package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whiterun/internal/media"
)

func TestOPT026RenewedProofSurvivesSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "bard.db")
	binary := filepath.Join(root, "extractor")
	calls := filepath.Join(root, "calls")
	script := "#!/bin/sh\necho verification >> '" + calls + "'\necho '{\"id\":\"abcdefghijk\",\"title\":\"Song\",\"track\":\"Song\",\"channel\":\"Artist - Topic\",\"artists\":[\"Artist\"],\"duration\":210}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	tr := media.Track{SpotifyID: "identity", RequestedSource: "spotify", Title: "Song", Artist: "Artist", Artists: []string{"Artist"}, Duration: 210 * time.Second, VideoID: "abcdefghijk", URL: "https://www.youtube.com/watch?v=abcdefghijk", ResumeOffset: 50 * time.Second}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := media.Candidate{ID: tr.VideoID, Title: tr.Title, Track: tr.Title, Artists: tr.Artists, Duration: 210, Channel: "Artist - Topic", CheckedAt: time.Now().Add(-11 * time.Minute)}
	if err := db.SaveMapping(ctx, media.MappingKey(tr, nil), old); err != nil {
		t.Fatal(err)
	}
	r := media.Resolver{Binary: binary, Cache: &media.SearchCache{}, Mappings: db}
	renewed, err := r.VerifiedSpotify(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ResumeOffset != tr.ResumeOffset || renewed.SpotifyID != tr.SpotifyID {
		t.Fatal("caller identity/checkpoint changed")
	}
	c, ok, err := db.LoadMapping(ctx, media.MappingKey(tr, nil))
	if err != nil || !ok || !c.CheckedAt.After(old.CheckedAt) {
		t.Fatal("renewal not persisted", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r = media.Resolver{Binary: binary, Cache: &media.SearchCache{}, Mappings: db}
	if _, err := r.VerifiedSpotify(ctx, tr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(raw), "verification") != 1 {
		t.Fatal("reopened store re-extracted proof", string(raw), err)
	}
	if err := db.CheckHealth(ctx); err != nil {
		t.Fatal(err)
	}
}
