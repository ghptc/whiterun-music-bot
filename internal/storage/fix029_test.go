package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestFIX029SnapshotKeepsMetadataAndOccurrences(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	track := media.Track{Title: "same recording", Duration: time.Minute}
	state := player.DurableState{Current: &track, Queue: []media.Track{track, track}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	track.ResumeOffset = 20 * time.Millisecond
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	var writes int
	if err := db.db.QueryRow("SELECT count(*) FROM tracks").Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("metadata not deduplicated: tracks=%d err=%v", writes, err)
	}
	loaded, err := db.LoadGuild(ctx, "1")
	if err != nil || loaded.Current == nil || loaded.Current.ResumeOffset != 20*time.Millisecond || len(loaded.Queue) != 2 || loaded.Queue[0].Title != track.Title || loaded.Queue[1].Title != track.Title {
		t.Fatalf("checkpoint or duplicate positions changed: %+v %v", loaded, err)
	}
	track.Title = "corrected metadata"
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow("SELECT count(*) FROM tracks").Scan(&writes); err != nil || writes != 2 {
		t.Fatalf("new metadata was not persisted: tracks=%d err=%v", writes, err)
	}
	var integrity string
	if err := db.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
}

func TestFIX029LateTrackFailureRollsBackWholeSnapshot(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	initial := player.DurableState{Queue: []media.Track{{Title: "original"}}}
	if err := db.SaveGuild(ctx, "1", initial); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_late BEFORE INSERT ON tracks WHEN instr(NEW.metadata,'reject-late')>0 BEGIN SELECT RAISE(ABORT,'injected late failure'); END`); err != nil {
		t.Fatal(err)
	}
	changed := player.DurableState{Queue: make([]media.Track, 500)}
	for i := range changed.Queue {
		changed.Queue[i] = media.Track{Title: fmt.Sprintf("track %d", i), Position: i + 1}
	}
	changed.Queue[499].Title = "reject-late"
	if err := db.SaveGuild(ctx, "1", changed); err == nil {
		t.Fatal("late fault was ignored")
	}
	loaded, err := db.LoadGuild(ctx, "1")
	if err != nil || len(loaded.Queue) != 1 || loaded.Queue[0].Title != "original" {
		t.Fatalf("failed snapshot replaced queue: %+v %v", loaded, err)
	}
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM tracks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("earlier batches escaped transaction rollback: %d %v", count, err)
	}
}

func TestFIX029PlaybackCheckpointAndEventCommitAtomically(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	first, future := media.Track{Title: "current"}, media.Track{Title: "future"}
	state := player.DurableState{Queue: []media.Track{first, future, future}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_future BEFORE UPDATE ON tracks WHEN instr(NEW.metadata,'future')>0 BEGIN SELECT RAISE(ABORT,'queue metadata rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	current := first
	current.ResumeOffset = 20 * time.Millisecond
	state.Current, state.CurrentStarted = &current, true
	state.Queue, state.History = []media.Track{future, future}, []media.Track{first}
	if err := db.SavePlaybackCheckpoint(ctx, "1", state, &first); err != nil {
		t.Fatal(err)
	}
	current.ResumeOffset = 40 * time.Millisecond
	if err := db.SavePlaybackCheckpoint(ctx, "1", state, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM playback_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("resumed checkpoint duplicated play event: %d %v", count, err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_play BEFORE INSERT ON playback_events BEGIN SELECT RAISE(ABORT,'play event failure'); END`); err != nil {
		t.Fatal(err)
	}
	current.ResumeOffset = 60 * time.Millisecond
	if err := db.SavePlaybackCheckpoint(ctx, "1", state, &first); err == nil {
		t.Fatal("event failure ignored")
	}
	loaded, err := db.LoadGuild(ctx, "1")
	if err != nil || loaded.Current == nil || loaded.Current.ResumeOffset != 40*time.Millisecond || len(loaded.Queue) != 2 || len(loaded.History) != 1 {
		t.Fatalf("checkpoint partially committed or lost queue: %+v %v", loaded, err)
	}
	if err := db.db.QueryRow("SELECT count(*) FROM playback_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed transaction created an event: %d %v", count, err)
	}
}

func TestFIX029ResolutionDeltaPreservesUntouchedOccurrences(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	future := media.Track{Title: "future"}
	state := player.DurableState{Queue: []media.Track{{Title: "pending"}, future, future}}
	if err := db.SaveGuild(ctx, "1", state); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_future BEFORE UPDATE ON tracks WHEN instr(NEW.metadata,'future')>0 BEGIN SELECT RAISE(ABORT,'untouched metadata rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	state.Queue[0] = media.Track{Title: "verified", VideoID: "abcdefghijk"}
	if err := db.SaveResolvedGuild(ctx, "1", state, state.Queue[:1]); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.LoadGuild(ctx, "1")
	if err != nil || len(loaded.Queue) != 3 || loaded.Queue[0].Title != "verified" || loaded.Queue[1].Title != "future" || loaded.Queue[2].Title != "future" {
		t.Fatalf("resolution changed ordering/duplicates: %+v %v", loaded, err)
	}
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM tracks").Scan(&count); err != nil || count != 3 {
		t.Fatalf("changed metadata was not stored: %d %v", count, err)
	}
}

func TestFIX029LateCatalogBatchFailurePreservesQuotaAndMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	tracks := make([]media.Track, 500)
	for i := range tracks {
		tracks[i] = media.Track{RequestedSource: "spotify", SpotifyID: fmt.Sprintf("%022d", i+1), Title: fmt.Sprintf("track %d", i)}
	}
	original := tracks[0]
	original.Title = "original"
	if err := db.SaveCatalog(ctx, original); err != nil {
		t.Fatal(err)
	}
	tracks[499].MetadataPending = true
	if err := db.SaveCatalogBatch(ctx, tracks); err == nil {
		t.Fatal("late invalid metadata was accepted")
	}
	loaded, ok, err := db.LoadCatalog(ctx, original.SpotifyID)
	if err != nil || !ok || loaded.Title != "original" {
		t.Fatalf("failed batch changed existing metadata: %+v %v", loaded, err)
	}
	var count, monitoring int
	if err := db.db.QueryRow("SELECT count(*) FROM music_catalog").Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial catalog batch committed: %d %v", count, err)
	}
	if err := db.db.QueryRow("SELECT monitoring FROM catalog_quota WHERE singleton=1").Scan(&monitoring); err != nil || monitoring != 1 {
		t.Fatalf("quota monitoring changed: %d %v", monitoring, err)
	}
}

func TestFIX029UnconfirmedSelectionRemainsDurableAndRestorable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	entered := make(chan struct{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(ctx, log, func(ctx context.Context, _ media.Track, _ player.Voice) error {
		close(entered)
		<-ctx.Done() // no frame was accepted before this simulated interruption
		return ctx.Err()
	})
	defer p.Close(ctx)
	if err := p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	first := media.Track{Title: "duplicate", URL: "https://www.youtube.com/watch?v=AbCdEfGhijk"}
	if err := p.EnqueueMany([]media.Track{first, first, {Title: "next"}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("selection did not enter playback")
	}
	saved, err := db.LoadGuild(ctx, "1")
	if err != nil || saved.Current != nil || len(saved.Queue) != 3 || len(saved.History) != 0 {
		t.Fatalf("unconfirmed selection consumed durable position: %+v %v", saved, err)
	}
	restoreCtx, cancel := context.WithCancel(ctx)
	restored := player.New(restoreCtx, log, func(context.Context, media.Track, player.Voice) error {
		t.Error("restoration started audio without Voice")
		return nil
	})
	defer func() { cancel(); restored.Close(ctx) }()
	if err := restored.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	state := restored.State()
	if state.Current != nil || len(state.Queued) != 3 || len(restored.History()) != 0 {
		t.Fatalf("restoration lost or duplicated an occurrence: %+v", state)
	}
	for i, track := range state.Queued {
		if track.Position != i+1 || track.ResumeOffset != 0 || (i < 2 && track.URL != first.URL) {
			t.Fatalf("restoration changed queue identity/order: %+v", state.Queued)
		}
	}
}
