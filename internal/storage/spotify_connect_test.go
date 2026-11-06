package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestSpotifyConnectQueueSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spotify.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	track := media.Track{RequestedSource: "spotify_connect", SpotifyID: "0123456789ABCDEFGHIJKL", URL: "spotify:track:0123456789ABCDEFGHIJKL", Title: "Radio Edit", Artists: []string{"Artist"}, Duration: 3 * time.Minute, ResumeOffset: 42 * time.Second}
	next := track
	next.ResumeOffset = 0
	next.Position = 2
	state := player.DurableState{Current: &track, CurrentStarted: true, Queue: []media.Track{next, next}}
	if err = db.SaveGuild(context.Background(), "guild-one", state); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved, err := db.LoadGuild(context.Background(), "guild-one")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Current == nil || saved.Current.SpotifyID != track.SpotifyID || saved.Current.RequestedSource != "spotify_connect" || saved.Current.URL != track.URL || saved.Current.ResumeOffset != 42*time.Second || len(saved.Queue) != 2 || saved.Queue[0].SpotifyID != track.SpotifyID || saved.Queue[1].Position != 2 {
		t.Fatalf("native identity, duplicate or checkpoint lost: %+v", saved)
	}
	other, err := db.LoadGuild(context.Background(), "guild-two")
	if err != nil || other.Current != nil || len(other.Queue) != 0 {
		t.Fatal("guild isolation lost", other, err)
	}
}
