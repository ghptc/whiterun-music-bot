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
	// Expire the availability lease before simulating changed provider metadata.
	candidate.CheckedAt = time.Now().Add(-11 * time.Minute)
	if err := db.SaveMapping(ctx, key, candidate); err != nil {
		t.Fatal(err)
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

func TestFreshPersistentMappingAvoidsMetadataProcessAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	track := media.Track{SpotifyID: "spotify-id", Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	c := media.Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist - Topic", Duration: 210, CheckedAt: time.Now()}
	key := media.MappingKey(track, nil)
	if !strings.HasPrefix(key, "spotify:spotify-id:") {
		t.Fatal(key)
	}
	if err := db.SaveMapping(ctx, key, c); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := media.Resolver{Binary: "/nonexistent/bard-test-yt-dlp", Mappings: db, Match: &track}
	got, err := r.Resolve(ctx, "Artist Song")
	if err != nil || got.VideoID != c.ID {
		t.Fatal(got, err)
	}
}

func TestFirstConfirmedMappingIsPersistedAndReused(t *testing.T) {
	db := openTest(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "yt-dlp")
	calls := filepath.Join(dir, "calls")
	first := media.Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Verified: true, Duration: 210}
	second := first
	second.ID = "lmnopqrstuv"
	entries, _ := json.Marshal(map[string]any{"entries": []media.Candidate{first, second}})
	full, _ := json.Marshal(first)
	script := "#!/bin/sh\nfor arg do target=\"$arg\"; done\nprintf '%s\\n' \"$target\" >> '" + calls + "'\ncase \"$target\" in\n*watch?v=abcdefghijk)\ncat <<'JSON'\n" + string(full) + "\nJSON\n;;\n*watch?v=*) exit 19;;\n*)\ncat <<'JSON'\n" + string(entries) + "\nJSON\n;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	original := media.Track{SpotifyID: "requested-id", Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	resolver := media.CollectionResolver{YouTube: media.Resolver{Binary: binary, Mappings: db}}
	ctx := context.Background()
	got, err := resolver.MatchSpotify(ctx, original, true)
	if err != nil || got.VideoID != first.ID || got.SpotifyID != original.SpotifyID {
		t.Fatal(got, err)
	}
	saved, ok, err := db.LoadMapping(ctx, media.MappingKey(original, nil))
	if err != nil || !ok || saved.ID != first.ID || saved.CheckedAt.IsZero() {
		t.Fatal(saved, ok, err)
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "\n") != 2 || strings.Contains(string(raw), "watch?v="+second.ID) {
		t.Fatal(string(raw))
	}
	// A fresh persisted result must survive an unavailable metadata executable.
	resolver.YouTube.Binary = "/nonexistent/bard-test-yt-dlp"
	original.Position, original.Total = 12, 123
	cached, err := resolver.MatchSpotify(ctx, original, true)
	if err != nil || cached.VideoID != first.ID || cached.Position != 12 || cached.SpotifyID != original.SpotifyID {
		t.Fatal(cached, err)
	}
}
