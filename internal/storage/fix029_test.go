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
