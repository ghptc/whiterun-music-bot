package qasoak

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type recoveryVoice struct{}

func (recoveryVoice) WriteFrame(context.Context, []byte) error { return nil }
func (recoveryVoice) Speaking(context.Context, bool) error     { return nil }
func (recoveryVoice) Close(context.Context)                    {}
func TestPendingCollectionRestartRestoresMetadataWithoutVoice(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "qa.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(context.Background(), log, nil)
	defer p.Close(context.Background())
	if err = p.ConfigureStore(db, "one"); err != nil {
		t.Fatal(err)
	}
	if err = p.Attach(recoveryVoice{}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	tracks := []media.Track{{Title: "original", SpotifyID: "identity", SpotifyArtworkURL: "https://qa.invalid/art", VerifyBeforePlay: true, Artists: []string{"artist"}}}
	job, err := p.BeginCollection(context.Background(), media.Collection{Tracks: tracks}, func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return media.Track{}, ctx.Err()
	}, p.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("resolution did not start")
	}
	if err = db.SaveSettings(context.Background(), "one", map[string]string{"announcement_channel": "99"}); err != nil {
		t.Fatal(err)
	}
	candidate := media.Candidate{ID: "abcdefghijk", Title: "original"}
	if err = db.SaveMapping(context.Background(), "identity", candidate); err != nil {
		t.Fatal(err)
	}
	p.Close(context.Background())
	select {
	case <-job.Done:
	default:
		t.Fatal("resolver survived Close")
	}
	restored := player.New(context.Background(), log, nil)
	defer restored.Close(context.Background())
	if err = restored.ConfigureStore(db, "one"); err != nil {
		t.Fatal(err)
	}
	state := restored.State()
	if state.Current != nil || len(state.Queued) != 1 || state.Queued[0].SpotifyID != "identity" || state.Queued[0].SpotifyArtworkURL != tracks[0].SpotifyArtworkURL || !state.Queued[0].VerifyBeforePlay {
		t.Fatal("pending metadata/identity/artwork recovery failed", state)
	}
	if err = restored.Enqueue(media.Track{Title: "must not attach itself"}, restored.Ticket()); err == nil {
		t.Fatal("recovery silently attached voice")
	}
	settings, err := db.LoadSettings(context.Background(), "one")
	if err != nil || settings["announcement_channel"] != "99" {
		t.Fatal(settings, err)
	}
	got, hit, err := db.LoadMapping(context.Background(), "identity")
	if err != nil || !hit || !reflect.DeepEqual(got, candidate) {
		t.Fatal(got, hit, err)
	}
	other, err := db.LoadGuild(context.Background(), "two")
	if err != nil || len(other.Queue) != 0 {
		t.Fatal("guild isolation failed", other, err)
	}
}
func TestOwnedDirectoryCannotBeReusedImplicitly(t *testing.T) {
	err := Run(context.Background(), Config{Out: t.TempDir(), Duration: time.Second, Sample: time.Second, Audio: 20 * time.Millisecond, Playlist: 123, Guilds: 1})
	if err == nil {
		t.Fatal("arbitrary existing directory accepted")
	}
}

func TestDiagnosticCachePolicyCannotChangeOnResume(t *testing.T) {
	out := t.TempDir()
	c := Config{Out: out, Duration: time.Second, Sample: time.Second, Audio: time.Second, Playlist: 500, Guilds: 2}
	if err := atomicJSON(filepath.Join(out, "qa010-owned.json"), c); err != nil {
		t.Fatal(err)
	}
	c.Resume = true
	c.DisableCache = true
	if err := Run(context.Background(), c); err == nil || err.Error() != "resume configuration mismatch" {
		t.Fatalf("cache policy changed for checkpoint: %v", err)
	}
}

func TestDisabledCacheCannotProduceWarmHitMetrics(t *testing.T) {
	out := filepath.Join(t.TempDir(), "not-created")
	err := Run(context.Background(), Config{Out: out, Duration: time.Second, Sample: time.Second, Audio: time.Second, Playlist: 500, Guilds: 2, Warm: true, DisableCache: true})
	if err == nil || err.Error() != "disabled QA cache cannot report warm cache hits" {
		t.Fatalf("misleading cache hit metrics accepted: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("invalid diagnostic created output: %v", err)
	}
}
