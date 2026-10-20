package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func autoPilotFixture(t *testing.T) (Track, Candidate, searchResult) {
	t.Helper()
	read := func(name string, v any) {
		t.Helper()
		raw, err := os.ReadFile("testdata/auto-pilot/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	var raw spotifyTrack
	var full Candidate
	var search searchResult
	read("spotify", &raw)
	read("mhXmqO7hCfA", &full)
	read("video-search", &search)
	track, ok := spotifyMetadata(&raw)
	if !ok {
		t.Fatal("invalid captured Spotify metadata")
	}
	return track, full, search
}

func TestAutoPilotRealAlbumVersion(t *testing.T) {
	track, full, search := autoPilotFixture(t)
	if track.SpotifyID != "3hWu4Z5IkREyWNT4zuuXM5" || track.Duration.Milliseconds() != 240986 || track.ISRC != "USIR10000329" {
		t.Fatal(track)
	}
	for _, c := range search.Entries {
		t.Logf("discovery %s %q: %+v", c.ID, c.Title, rankSpotify(track, *c, true))
	}
	checks := 0
	got, err := selectSpotify(context.Background(), track, deduplicateCandidates(search.Entries), func(c Candidate) (Candidate, error) {
		checks++
		if c.ID != full.ID {
			return Candidate{}, fmt.Errorf("no full fixture for nonselected candidate %s", c.ID)
		}
		return full, nil
	})
	if err != nil || got.ID != full.ID || checks != 1 {
		t.Fatalf("selected=%s checks=%d err=%v provisional=%+v full=%+v", got.ID, checks, err, rankSpotify(track, *search.Entries[0], true), CompareSpotify(track, full, DefaultMatchPolicy()))
	}
}

func TestAutoPilotAlbumVersionDoesNotRelaxIdentity(t *testing.T) {
	track, full, _ := autoPilotFixture(t)
	for _, variant := range []string{"cover", "live", "remix", "duration", "artist", "album-title-extra"} {
		t.Run(variant, func(t *testing.T) {
			bad := full
			switch variant {
			case "cover", "live", "remix":
				bad.Title += " " + variant
				bad.Track += " " + variant
			case "duration":
				bad.Duration = 520
			case "artist":
				bad.Artists = []string{"Cover Band"}
				bad.Artist = "Cover Band"
			case "album-title-extra":
				bad.Title = "Auto Pilot (Album Version) Another Song"
				bad.Track = bad.Title
			}
			if decision := CompareSpotify(track, bad, DefaultMatchPolicy()); decision.Status == MatchConfirmed {
				t.Fatal("incompatible identity accepted", decision)
			}
		})
	}
}

func TestAutoPilotRealMusicDiscoveryFirstConfirmed(t *testing.T) {
	track, full, _ := autoPilotFixture(t)
	raw, err := os.ReadFile("testdata/auto-pilot/music-search.json")
	if err != nil {
		t.Fatal(err)
	}
	var pool searchResult
	if err = json.Unmarshal(raw, &pool); err != nil {
		t.Fatal(err)
	}
	searches, checks := 0, 0
	got, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) { searches++; return pool.Entries, nil }, func(c Candidate) (Candidate, error) {
		checks++
		if c.ID != full.ID {
			return Candidate{}, fmt.Errorf("uncaptured candidate %s", c.ID)
		}
		return full, nil
	})
	if err != nil || got.ID != full.ID || searches != 1 || checks != 1 {
		t.Fatal(got.ID, err, searches, checks)
	}
}

func TestAutoPilotResolutionCache(t *testing.T) {
	track, full, _ := autoPilotFixture(t)
	// Controlled extraction retains captured full metadata; no network or stream URL.
	raw, err := json.Marshal(searchResult{Entries: []*Candidate{&full}})
	if err != nil {
		t.Fatal(err)
	}
	resolver, calls := candidateResolverFixture(t, string(raw))
	resolver.Match = &track
	resolver.Cache = &SearchCache{}
	for attempt := 0; attempt < 2; attempt++ {
		got, err := resolver.Resolve(context.Background(), spotifyQuery(track))
		if err != nil || got.VideoID != full.ID {
			t.Fatal(got, err)
		}
	}
	raw, err = os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "watch?v="); n != 1 {
		t.Fatal("cache re-extracted recording", n, string(raw))
	}
}
