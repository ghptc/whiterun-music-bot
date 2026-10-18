package media

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProductionRegressions(t *testing.T) {
	for _, tc := range []struct{ id, want string }{{"55VzNhbcf0Gxf6qRCUAV01", "SzVSTyxQ0rk"}, {"3bC1ahPIYt1btJzSSEyyrF", "MOIwxOYdhrk"}, {"6Knv6wdA0luoMUuuoYi2i1", "z9boCCK7M8c"}, {"3SMVk8c3YOVv6DcTn8ABXa", "55hB6dwU3yQ"}} {
		id := tc.id
		t.Run(id, func(t *testing.T) {
			read := func(name string, dst any) {
				t.Helper()
				b, e := os.ReadFile("testdata/production/" + name + ".json")
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(b, dst); e != nil {
					t.Fatal(e)
				}
			}
			var raw spotifyTrack
			read(id, &raw)
			track, ok := spotifyMetadata(&raw)
			if !ok {
				t.Fatal("invalid Spotify metadata")
			}
			t.Logf("Spotify identity: %s — %s", track.Artist, track.Title)
			searches, checks := 0, 0
			c, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(target, purpose string) ([]*Candidate, error) {
				searches++
				suffix := "video"
				if strings.Contains(target, "music.youtube.com") {
					suffix = "music"
				} else if searches == 3 {
					suffix = "alternative"
				} else if searches == 4 {
					suffix = "topic"
				}
				if id == "3SMVk8c3YOVv6DcTn8ABXa" && searches <= 2 {
					suffix = "production_" + suffix
				}
				var pool searchResult
				read(id+"_"+suffix, &pool)
				return pool.Entries, nil
			}, func(c Candidate) (Candidate, error) {
				checks++
				var full Candidate
				read(c.ID, &full)
				t.Logf("candidate %s %q: %+v", c.ID, full.Title, rankSpotify(track, full, false))
				return full, nil
			})
			t.Logf("selected=%s error=%v searches=%d checks=%d", c.ID, err, searches, checks)
			if searches > 4 || checks > 8 {
				t.Fatal("budget exceeded")
			}
			if err != nil || c.ID != tc.want {
				t.Fatalf("selected=%s want=%s err=%v", c.ID, tc.want, err)
			}
			if CompareSpotify(track, c, DefaultMatchPolicy()).Status != MatchConfirmed {
				t.Fatal("unverified result")
			}
		})
	}
}
