package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
)

func TestValidatedMappingReusedAndInconsistentVideoInvalidated(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	track := media.Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second, RequestedSource: "spotify", SpotifyID: "id"}
	candidate := media.Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist - Topic", Duration: 210}
	key := media.MappingKey(track, nil)
	dir := t.TempDir()
	output := filepath.Join(dir, "metadata.json")
	calls := filepath.Join(dir, "calls")
	binary := filepath.Join(dir, "yt-dlp")
	// The controlled executable replaces network extraction, while mappings use
	// the actual on-disk SQLite implementation used by the bot.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + calls + "'\ncat '" + output + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(c media.Candidate) {
		t.Helper()
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(candidate)
	if err := db.SaveMapping(ctx, key, candidate); err != nil {
		t.Fatal(err)
	}
	resolver := media.Resolver{Binary: binary, Match: &track, Mappings: db}
	got, err := resolver.Resolve(ctx, "Artist Song")
	if err != nil || got.VideoID != candidate.ID {
		t.Fatal(got, err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil || strings.Contains(string(raw), "ytsearch") || !strings.Contains(string(raw), "watch?v=abcdefghijk") {
		t.Fatal(string(raw), err)
	}
	candidate.Title = "Song Live"
	write(candidate)
	if _, err = resolver.Resolve(ctx, "Artist Song"); err == nil {
		t.Fatal("accepted stale incompatible mapping")
	}
	if _, ok, err := db.LoadMapping(ctx, key); err != nil || ok {
		t.Fatal("mapping not invalidated", ok, err)
	}
	track.URL = "https://www.youtube.com/watch?v=abcdefghijk"
	track.VideoID = "abcdefghijk"
	if resolver.VerifySpotify(ctx, track) == nil {
		t.Fatal("playback verification accepted incompatible video")
	}
	candidate.Title = "Song"
	write(candidate)
	if err := resolver.VerifySpotify(ctx, track); err != nil {
		t.Fatal(err)
	}
}
