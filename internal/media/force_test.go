package media

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type forceSpotify struct{ t *testing.T }

func (s forceSpotify) Collection(context.Context, string, string) (Collection, error) {
	s.t.Fatal("Spotify called")
	return Collection{}, nil
}

type forceMappings struct{ t *testing.T }

func (s forceMappings) LoadMapping(context.Context, string) (Candidate, bool, error) {
	s.t.Fatal("mapping read")
	return Candidate{}, false, nil
}
func (s forceMappings) SaveMapping(context.Context, string, Candidate) error {
	s.t.Fatal("mapping write")
	return nil
}
func (s forceMappings) DeleteMapping(context.Context, string) error {
	s.t.Fatal("mapping delete")
	return nil
}

func TestForceDirectSelection(t *testing.T) {
	for _, query := range []string{
		"https://youtube.com/watch?v=abcdefghijk",
		"https://youtu.be/abcdefghijk",
		"https://music.youtube.com/watch?v=abcdefghijk",
		"https://youtube.com/watch?v=abcdefghijk&list=PLignored&index=8&t=42",
	} {
		t.Run(query, func(t *testing.T) {
			r, args := resolverFixture(t, `{"id":"abcdefghijk","title":"Interview unrelated to Paranoid","duration":17,"thumbnail":"https://i.ytimg.com/vi/abcdefghijk/default.jpg"}`)
			script, readErr := os.ReadFile(r.Binary)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err := os.WriteFile(r.Binary, []byte(strings.Replace(string(script), " > '", " >> '", 1)), 0700); err != nil {
				t.Fatal(err)
			}
			r.Match = &Track{SpotifyID: "known", Title: "Paranoid", Artist: "Black Sabbath", Duration: 171 * time.Second}
			r.Mappings = forceMappings{t}
			r.Cache = &SearchCache{}
			cr := CollectionResolver{YouTube: r, Spotify: forceSpotify{t}, Search: func(context.Context, string) (Track, error) { t.Fatal("alternative search"); return Track{}, nil }}
			c, progressive, err := cr.PreparePlay(context.Background(), query, true)
			if err != nil || progressive || len(c.Tracks) != 1 {
				t.Fatal(c, progressive, err)
			}
			tr := c.Tracks[0]
			if !tr.ForcedYouTube || tr.VideoID != "abcdefghijk" || tr.Title != "Interview unrelated to Paranoid" || tr.Duration != 17*time.Second || tr.YouTubeThumbnailURL == "" || tr.RequestedSource != "youtube" || tr.SpotifyID != "" || tr.MatchStatus != "" {
				t.Fatal(tr)
			}
			calls, _ := os.ReadFile(args)
			if strings.Count(string(calls), "https://www.youtube.com/watch?v=abcdefghijk") != 1 || strings.Contains(string(calls), "ytsearch") || strings.Contains(string(calls), "--yes-playlist") || !strings.HasSuffix(string(calls), "https://www.youtube.com/watch?v=abcdefghijk\n") {
				t.Fatal(string(calls))
			}
			for key := range r.Cache.entries {
				if strings.HasPrefix(key, "spotify:") {
					t.Fatal("Spotify cache contaminated")
				}
			}
			// The force flag must not leak into the ordinary URL cache.
			r.Match = nil
			ordinary, err := r.Resolve(context.Background(), query)
			if err != nil || ordinary.ForcedYouTube {
				t.Fatal(ordinary, err)
			}
		})
	}
}
func TestForceRejectsInputsBeforeExtraction(t *testing.T) {
	for _, q := range []string{"Song Artist", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC", "https://youtube.com/playlist?list=PL123", "https://youtu.be/", "https://youtube.com/watch?v=bad", "https://youtube.com.evil.test/watch?v=abcdefghijk", "https://youtube.com@127.0.0.1/watch?v=abcdefghijk", "https://youtube.com:443/watch?v=abcdefghijk", "https://youtube.com/%zz", "file:///tmp/song", strings.Repeat("x", 501)} {
		t.Run(q, func(t *testing.T) {
			_, _, err := (CollectionResolver{YouTube: Resolver{Binary: "must-not-execute"}, Spotify: forceSpotify{t}}).PreparePlay(context.Background(), q, true)
			if !errors.Is(err, ErrForceURL) {
				t.Fatal(err)
			}
		})
	}
}
func TestForceFailureNeverSubstitutes(t *testing.T) {
	for _, output := range []string{`{"id":"different12","title":"Other"}`, `{"id":"abcdefghijk","live_status":"is_upcoming"}`, "invalid JSON"} {
		r, _ := resolverFixture(t, output)
		_, _, err := (CollectionResolver{YouTube: r, Search: func(context.Context, string) (Track, error) { t.Fatal("fallback"); return Track{}, nil }}).PreparePlay(context.Background(), "https://youtu.be/abcdefghijk", true)
		if err == nil {
			t.Fatal("failed video replaced or accepted")
		}
	}
	r, _ := resolverFixture(t, "")
	if err := os.WriteFile(r.Binary, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (CollectionResolver{YouTube: r}).PreparePlay(context.Background(), "https://youtu.be/abcdefghijk", true); err == nil {
		t.Fatal("extractor failure ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (CollectionResolver{YouTube: r}).PreparePlay(ctx, "https://youtu.be/abcdefghijk", true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
