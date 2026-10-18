package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// Captured 2026-10-18 from Spotify Web API and yt-dlp 2026.08.19.
// Never substitute invented identifiers or enrich missing YouTube metadata.
func TestRealSpotifyFalseRejectionInvestigation(t *testing.T) {
	for _, tc := range []struct{ id, song, want string }{
		{"3bC1ahPIYt1btJzSSEyyrF", "Whistle", "MOIwxOYdhrk"},
		{"6Knv6wdA0luoMUuuoYi2i1", "My_House", "z9boCCK7M8c"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			read := func(name string, dst any) {
				t.Helper()
				b, e := os.ReadFile("testdata/fidelity/" + name + ".json")
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(b, dst); e != nil {
					t.Fatal(e)
				}
			}
			var raw spotifyTrack
			read(tc.id, &raw)
			track, ok := spotifyMetadata(&raw)
			if !ok {
				t.Fatal("invalid real Spotify fixture")
			}
			verify := func(c Candidate) (Candidate, error) {
				var full Candidate
				read(c.ID, &full)
				t.Logf("verified %s: %+v", c.ID, rankSpotify(track, full, false))
				return full, nil
			}
			var music, video searchResult
			read(tc.song+"_music", &music)
			read(tc.song+"_video", &video)
			for _, c := range music.Entries {
				c.MusicSearch = true
			}
			pool := deduplicateCandidates(music.Entries, video.Entries)
			for _, c := range pool {
				t.Logf("discovery %s: %+v", c.ID, rankSpotify(track, c, true))
			}
			got, err := selectSpotify(context.Background(), track, pool, verify)
			t.Logf("initial selected=%s err=%v", got.ID, err)
			calls := 0
			got, err = discoverSpotify(context.Background(), track, DefaultMatchPolicy(), track.Artist+" "+track.Title, func(target, purpose string) ([]*Candidate, error) {
				calls++
				if strings.Contains(target, "music.youtube.com") {
					return music.Entries, nil
				}
				if calls == 2 {
					return video.Entries, nil
				}
				var alt searchResult
				suffix := "_alternative"
				if calls == 4 {
					suffix = "_release"
				}
				read(tc.song+suffix, &alt)
				return alt.Entries, nil
			}, verify)
			t.Logf("progressive selected=%s err=%v", got.ID, err)
			if tc.want != "" {
				if err != nil || got.ID != tc.want {
					t.Fatal(got, err)
				}
			} else {
				var decision *MatchError
				if !errors.As(err, &decision) || decision.Status != MatchUncertain || decision.Reason != "ambiguous_candidates" {
					t.Fatalf("unexpected decision: %+v %v", got, err)
				}
			}
		})
	}
}
