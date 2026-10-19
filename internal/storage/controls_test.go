package storage

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestControlsRestartAndHistoryIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bard.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(ctx, log, nil)
	if err := p.ConfigureStore(db, "a"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLoop("queue"); err != nil {
		t.Fatal(err)
	}
	p.SetAutoplay(true)
	if err := db.SaveGuild(ctx, "b", player.DurableState{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := db.RecordPlayback(ctx, id, media.Track{Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	p.Close(ctx)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored := player.New(ctx, log, nil)
	defer restored.Close(ctx)
	if err := restored.ConfigureStore(db, "a"); err != nil {
		t.Fatal(err)
	}
	state := restored.State()
	if state.Loop != "queue" || !state.Autoplay {
		t.Fatal(state)
	}
	tracks, err := db.PlaybackHistory(ctx, "a", 10)
	if err != nil || len(tracks) != 1 || tracks[0].Title != "a" {
		t.Fatal(tracks, err)
	}
	other, err := db.LoadGuild(ctx, "b")
	if err != nil || other.Autoplay || other.Loop != "" {
		t.Fatal(other, err)
	}
	if restored.State().Current != nil || restored.Length() != 0 {
		t.Fatal("history or autoplay started playback")
	}
}

func TestEditedQueueSurvivesRestart(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	tracks := []media.Track{}
	for _, id := range []string{"a", "b", "c"} {
		tracks = append(tracks, media.Track{SpotifyID: id, Title: id, RequestedBy: "requester", SpotifyArtworkURL: "https://example.com/cover", Artists: []string{"artist"}})
	}
	if err := db.SaveGuild(ctx, "edits", player.DurableState{Queue: tracks}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := player.New(ctx, log, nil)
	if err := p.ConfigureStore(db, "edits"); err != nil {
		t.Fatal(err)
	}
	if err := p.Move(3, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(2); err != nil {
		t.Fatal(err)
	}
	p.Close(ctx)
	p = player.New(ctx, log, nil)
	defer p.Close(ctx)
	if err := p.ConfigureStore(db, "edits"); err != nil {
		t.Fatal(err)
	}
	state := p.State()
	if len(state.Entries) != 2 || state.Entries[0].Track.SpotifyID != "c" || state.Entries[1].Track.SpotifyID != "b" {
		t.Fatal(state.Entries)
	}
	for _, e := range state.Entries {
		if e.Track.RequestedBy != "requester" || e.Track.SpotifyArtworkURL == "" || e.Track.Artists[0] != "artist" {
			t.Fatal(e)
		}
	}
}
