package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func TestSQLiteMigrationsTransactionsAndIsolation(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := db.db.QueryRow("SELECT count(*) FROM migrations").Scan(&versions); err != nil || versions != 2 {
		t.Fatal(versions, err)
	}
	state := player.DurableState{Queue: []media.Track{{Title: "first", SpotifyID: "original", Artists: []string{"artist"}}, {Title: "first", SpotifyID: "original"}}, History: []media.Track{{Title: "past"}}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(ctx, "1", map[string]string{"announcement_channel": "5"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveGuild(ctx, "2", player.DurableState{Queue: []media.Track{{Title: "other guild"}}}); err != nil {
		t.Fatal(err)
	}
	settings, err := db.LoadSettings(ctx, "1")
	if err != nil || settings["announcement_channel"] != "5" {
		t.Fatal(settings, err)
	}
	// Abort after state replacement: the entire transaction must roll back.
	if _, err := db.db.Exec("CREATE TRIGGER reject_history BEFORE INSERT ON history BEGIN SELECT RAISE(ABORT,'injected'); END"); err != nil {
		t.Fatal(err)
	}
	state.Queue[0].Title = "must rollback"
	if err := db.SaveGuild(ctx, "1", state); err == nil {
		t.Fatal("trigger failure lost")
	}
	got, err := db.LoadGuild(ctx, "1")
	if err != nil || got.Queue[0].Title != "first" || len(got.Queue) != 2 {
		t.Fatal(got, err)
	}
	other, err := db.LoadGuild(ctx, "2")
	if err != nil || other.Queue[0].Title != "other guild" {
		t.Fatal(other, err)
	}
	for _, query := range []string{"INSERT INTO history VALUES('missing',0,'{}')", "INSERT INTO guilds(guild_id,state) VALUES('bad','not-json')", "INSERT INTO mappings VALUES('key','{}',0)"} {
		if _, err := db.db.Exec(query); err == nil {
			t.Fatal("constraint not enforced", query)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if db.SaveGuild(canceled, "1", state) == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestSQLiteReopenAndMappingInvalidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bard.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state := player.DurableState{Current: &media.Track{Title: "current", SpotifyID: "id", Explicit: new(true)}, Queue: []media.Track{{Title: "next"}}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	candidate := media.Candidate{ID: "abcdefghijk", Title: "Song"}
	if err := db.SaveMapping(ctx, "identity", candidate); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.LoadGuild(ctx, "1")
	if err != nil || got.Current.SpotifyID != "id" || !*got.Current.Explicit || got.Queue[0].Title != "next" {
		t.Fatal(got, err)
	}
	cached, ok, err := db.LoadMapping(ctx, "identity")
	if err != nil || !ok || cached.ID != candidate.ID {
		t.Fatal(cached, ok, err)
	}
	if _, err := db.db.Exec("UPDATE mappings SET expires=?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.LoadMapping(ctx, "identity"); ok {
		t.Fatal("expired mapping reused")
	}
	if err := db.SaveMapping(ctx, "identity", candidate); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMapping(ctx, "identity"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.LoadMapping(ctx, "identity"); ok {
		t.Fatal("deleted mapping reused")
	}
}

type voice struct{}

func (voice) WriteFrame(context.Context, []byte) error { return nil }
func (voice) Speaking(context.Context, bool) error     { return nil }
func (voice) Close(context.Context)                    {}
func TestSQLitePlayerRecoveryWithoutVoice(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan string, 4)
	play := func(ctx context.Context, track media.Track, v player.Voice) error {
		_ = v.WriteFrame(ctx, []byte{1})
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	}
	p := player.New(ctx, log, play)
	if err := p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	if err := p.EnqueueMany([]media.Track{{Title: "first"}, {Title: "second"}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no start")
	}
	p.Close(ctx)
	recovered := player.New(ctx, log, play)
	defer recovered.Close(ctx)
	if err := recovered.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	state := recovered.State()
	if state.Current != nil || len(state.Queued) != 2 || state.Queued[0].Title != "first" {
		t.Fatal(state)
	}
	select {
	case <-started:
		t.Fatal("auto attached voice")
	default:
	}
	if err := recovered.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != "first" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	recovered.Stop()
	recovered.Close(ctx)
	saved, err := db.LoadGuild(ctx, "1")
	if err != nil || saved.Current != nil || len(saved.Queue) != 0 {
		t.Fatal(saved, err)
	}
	events, err := db.PlaybackHistory(ctx, "1", 10)
	if err != nil || len(events) != 2 || events[0].Title != "first" {
		t.Fatal(events, err)
	}
}
func TestSQLiteConcurrentGuildTransactions(t *testing.T) {
	db := openTest(t)
	var wg sync.WaitGroup
	for guild := range 5 {
		wg.Go(func() {
			for i := range 10 {
				id := fmt.Sprint(guild)
				if err := db.SaveGuild(context.Background(), id, player.DurableState{Queue: []media.Track{{Title: fmt.Sprint(guild, i)}}}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	for guild := range 5 {
		got, err := db.LoadGuild(context.Background(), fmt.Sprint(guild))
		if err != nil || got.Queue[0].Title != fmt.Sprint(guild, 9) {
			t.Fatal(got, err)
		}
	}
}

func TestDurabilityFailureIsVisibleAndTransactionRetainsOldQueue(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(ctx, log, func(ctx context.Context, _ media.Track, _ player.Voice) error { <-ctx.Done(); return ctx.Err() })
	defer p.Close(ctx)
	if err := p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(media.Track{Title: "one"}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("CREATE TRIGGER reject_state BEFORE UPDATE ON guilds BEGIN SELECT RAISE(ABORT,'disk failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(media.Track{Title: "two"}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	if p.State().PersistenceError == "" {
		t.Fatal("durability failure hidden")
	}
	saved, err := db.LoadGuild(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range saved.Queue {
		if track.Title == "two" {
			t.Fatal("failed transaction committed")
		}
	}
}

func TestPlaybackHistoryIsAppendOnlyAndGuildScoped(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	for _, id := range []string{"1", "2"} {
		if err := db.SaveGuild(ctx, id, player.DurableState{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, title := range []string{"one", "two", "one"} {
		if err := db.RecordPlayback(ctx, "1", media.Track{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordPlayback(ctx, "2", media.Track{Title: "other"}); err != nil {
		t.Fatal(err)
	}
	// Queue/history checkpoints never delete durable playback events.
	if err := db.SaveGuild(ctx, "1", player.DurableState{}); err != nil {
		t.Fatal(err)
	}
	history, err := db.PlaybackHistory(ctx, "1", 10)
	if err != nil || len(history) != 3 || history[0].Title != "one" || history[1].Title != "two" {
		t.Fatal(history, err)
	}
	other, err := db.PlaybackHistory(ctx, "2", 10)
	if err != nil || len(other) != 1 || other[0].Title != "other" {
		t.Fatal(other, err)
	}
	if _, err := db.PlaybackHistory(ctx, "1", 0); err == nil {
		t.Fatal("unbounded history query")
	}
}
func TestNewerSchemaRejected(t *testing.T) {
	db := openTest(t)
	if _, err := db.db.Exec("INSERT INTO migrations VALUES(999)"); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(context.Background()); err == nil {
		t.Fatal("accepted unknown newer schema")
	}
}

func TestRecoveryDuringPreparationPreservesPreviousHistory(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	state := player.DurableState{Current: &media.Track{Title: "preparing"}, History: []media.Track{{Title: "actually started"}}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(ctx, log, nil)
	defer p.Close(ctx)
	if err := p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	if history := p.History(); len(history) != 1 || history[0].Title != "actually started" {
		t.Fatal(history)
	}
}
