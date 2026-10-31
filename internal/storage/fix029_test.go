package storage

import (
	"context"
	"fmt"
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
