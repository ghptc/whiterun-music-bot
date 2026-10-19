package media

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func fastFixture(tb testing.TB) (Track, Candidate) {
	tb.Helper()
	read := func(name string, v any) {
		tb.Helper()
		raw, err := os.ReadFile("testdata/production/" + name + ".json")
		if err != nil {
			tb.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			tb.Fatal(err)
		}
	}
	var raw spotifyTrack
	read("3bC1ahPIYt1btJzSSEyyrF", &raw)
	track, ok := spotifyMetadata(&raw)
	if !ok {
		tb.Fatal("invalid fixture")
	}
	var c Candidate
	read("MOIwxOYdhrk", &c)
	return track, c
}

func TestFastMatchStopsAfterCorroboratedRelease(t *testing.T) {
	track, c := fastFixture(t)
	searches, checks := 0, 0
	got, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) {
		searches++
		return []*Candidate{&c}, nil
	}, func(v Candidate) (Candidate, error) { checks++; return v, nil })
	if err != nil || got.ID != c.ID || searches != 1 || checks != 1 {
		t.Fatalf("got=%s err=%v searches=%d checks=%d", got.ID, err, searches, checks)
	}
}

func TestFastMatchAdversarialAndSecondCandidate(t *testing.T) {
	track, release := fastFixture(t)
	for _, name := range []string{"cover", "remix", "wrong artist", "two second delta"} {
		t.Run(name, func(t *testing.T) {
			first := release
			first.ID = "abcdefghijk"
			switch name {
			case "cover":
				first.Title += " Cover"
				first.Track += " Cover"
			case "remix":
				first.Title += " Remix"
				first.Track += " Remix"
			case "wrong artist":
				first.Artists = []string{"Impostor"}
				first.Artist = "Impostor"
				first.Channel = "Impostor"
			case "two second delta":
				first.Duration = track.Duration.Seconds() + 2
			}
			got, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) {
				if name == "two second delta" {
					return []*Candidate{&first}, nil
				}
				return []*Candidate{&first, &release}, nil
			}, func(c Candidate) (Candidate, error) { return c, nil })
			want := release.ID
			if name == "two second delta" {
				want = first.ID
			}
			if err != nil || got.ID != want {
				t.Fatalf("got=%s want=%s err=%v", got.ID, want, err)
			}
		})
	}
}

func TestIdentityLeaseBoundariesAndTrackBinding(t *testing.T) {
	track, c := fastFixture(t)
	now := time.Now()
	for _, age := range []time.Duration{-time.Second, 0, identityLease - time.Nanosecond, identityLease, 24 * time.Hour} {
		c.CheckedAt = now.Add(-age)
		if got, want := freshIdentity(c, now), age >= 0 && age < identityLease; got != want {
			t.Fatalf("age %s fresh=%v", age, got)
		}
	}
	c.CheckedAt = now
	track.VideoID = c.ID
	track.URL = "https://www.youtube.com/watch?v=" + c.ID
	track.identityProof = &c
	r := Resolver{Binary: "/nonexistent/bard-test-yt-dlp"}
	if err := r.VerifySpotify(context.Background(), track); err != nil {
		t.Fatal("fresh identity spawned verification", err)
	}
	track.Title = "Different Song"
	if err := r.VerifySpotify(context.Background(), track); err == nil {
		t.Fatal("proof reused for different identity")
	}
}

func BenchmarkFastMatchRealFixture(b *testing.B) {
	track, c := fastFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) { return []*Candidate{&c}, nil }, func(v Candidate) (Candidate, error) { return v, nil })
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestMisleadingFirstSummaryRequiresSecondVerification(t *testing.T) {
	track, c := fastFixture(t)
	first := c
	first.ID = "abcdefghijk"
	checks := 0
	got, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) { return []*Candidate{&first, &c}, nil }, func(v Candidate) (Candidate, error) {
		checks++
		if v.ID == first.ID {
			v.Track += " Cover"
			v.Title += " Cover"
		}
		return v, nil
	})
	if err != nil || got.ID != c.ID || checks != 2 {
		t.Fatalf("got=%s err=%v checks=%d", got.ID, err, checks)
	}
}

func TestFirstConfirmationStillEnforcesRequestedRecordingIdentifier(t *testing.T) {
	track, c := fastFixture(t)
	other := c
	other.ID = "abcdefghijk"
	c.ISRC = "USAAA1200001"
	track.ISRC = "USAAA1200002"
	other.ISRC = "USAAA1200002"
	got, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), spotifyQuery(track), func(string, string) ([]*Candidate, error) { return []*Candidate{&c, &other}, nil }, func(v Candidate) (Candidate, error) { return v, nil })
	if err != nil || got.ID != other.ID {
		t.Fatal("requested recording identifier ignored", got, err)
	}
}

func BenchmarkCachedSpotifyIdentity(b *testing.B) {
	track, c := fastFixture(b)
	c.CheckedAt = time.Now()
	audio := Track{VideoID: c.ID, URL: "https://www.youtube.com/watch?v=" + c.ID, identityProof: &c}
	cache := &SearchCache{}
	r := Resolver{Cache: cache, Match: &track, Binary: "/nonexistent/bard-test-yt-dlp"}
	cache.put("spotify:"+MappingKey(track, nil), audio, time.Now())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := r.Resolve(context.Background(), spotifyQuery(track))
		if err != nil {
			b.Fatal(err)
		}
		original := track
		original.VideoID, original.URL, original.identityProof = got.VideoID, got.URL, got.identityProof
		if err := r.VerifySpotify(context.Background(), original); err != nil {
			b.Fatal(err)
		}
	}
}
