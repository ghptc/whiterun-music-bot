package media

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRecordingGroups(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Artists: []string{"Artist"}, Album: "Album", Duration: 210 * time.Second}
	base := Candidate{ID: "abcdefghijk", Title: "Artist - Song (Official Audio)", Track: "Song", Artists: []string{"Artist"}, Album: "Album", Channel: "Artist", Verified: true, Duration: 210}
	for _, tc := range []struct {
		name      string
		mutate    func(*Track, *Candidate, *Candidate)
		uncertain bool
	}{
		{"two official territories", func(_ *Track, a, b *Candidate) { a.ChannelID = "territory-a"; b.ChannelID = "territory-b" }, false},
		{"official and lyrics", func(_ *Track, a, b *Candidate) { b.Title = "Artist - Song (Lyric Video)" }, false},
		{"equivalent duration", func(_ *Track, a, b *Candidate) { b.Duration = 211 }, false},
		{"different recording ISRC", func(_ *Track, a, b *Candidate) { a.ISRC = "USAAA1200001"; b.ISRC = "USAAA1200002" }, true},
		{"missing album", func(_ *Track, a, b *Candidate) { a.Album = ""; b.Album = "" }, true},
		{"same title no structured credits", func(_ *Track, a, b *Candidate) { a.Artists = nil; b.Artists = nil }, true},
		{"unofficial copied metadata", func(_ *Track, a, b *Candidate) {
			a.Channel = "Archive"
			b.Channel = "Archive"
			a.Verified = false
			b.Verified = false
		}, true},
		{"distinct edits", func(_ *Track, a, b *Candidate) { a.Duration = 207; b.Duration = 213 }, true},
		{"uncertain remaster", func(tr *Track, a, b *Candidate) {
			tr.Title = "Song Remaster"
			a.Title = tr.Title
			b.Title = tr.Title
			a.Track = tr.Title
			b.Track = tr.Title
		}, true},
		{"contradictory explicit", func(_ *Track, a, b *Candidate) { a.Explicit = new(true); b.Explicit = new(false) }, true},
		{"shared identifier", func(_ *Track, a, b *Candidate) { a.ISRC = "USAAA1200001"; b.ISRC = a.ISRC; a.Album = ""; b.Album = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := track
			a, b := base, base
			b.ID = "lmnopqrstuv"
			tc.mutate(&tr, &a, &b)
			for _, pool := range [][]Candidate{{a, b}, {b, a}, {a, a, b}} {
				got, err := selectSpotify(context.Background(), tr, pool, func(c Candidate) (Candidate, error) { return c, nil })
				if tc.uncertain {
					var m *MatchError
					if !errors.As(err, &m) || m.Status != MatchUncertain {
						t.Fatalf("got %v %v", got, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProgressiveSpotifyDiscovery(t *testing.T) {
	tr := Track{Title: "Song", Artist: "Artist", Album: "Album", Duration: 210 * time.Second}
	good := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Duration: 210}
	calls, checks := 0, 0
	got, err := discoverSpotify(context.Background(), tr, DefaultMatchPolicy(), "initial", func(target, purpose string) ([]*Candidate, error) {
		calls++
		if calls == 3 {
			return []*Candidate{&good}, nil
		}
		return nil, nil
	}, func(c Candidate) (Candidate, error) { checks++; return c, nil })
	if err != nil || got.ID != good.ID || calls != 3 || checks != 1 {
		t.Fatal(got, err, calls, checks)
	}
	calls = 0
	_, err = discoverSpotify(context.Background(), tr, DefaultMatchPolicy(), "initial", func(target, purpose string) ([]*Candidate, error) { calls++; return nil, nil }, func(c Candidate) (Candidate, error) { t.Fatal("empty discovery verified"); return c, nil })
	if calls != 4 || err == nil {
		t.Fatal(calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_, err = discoverSpotify(ctx, tr, DefaultMatchPolicy(), "initial", func(string, string) ([]*Candidate, error) { calls++; return nil, nil }, func(c Candidate) (Candidate, error) { return c, nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(calls, err)
	}
}

func TestRecordingGroupingCannotBridgeContradictions(t *testing.T) {
	tr := Track{Title: "Song", Artist: "Artist", Album: "Album", Duration: 210 * time.Second}
	a := Candidate{ID: "abcdefghijk", Title: "Song", Track: "Song", Artists: []string{"Artist"}, Album: "Album", Channel: "Artist", Verified: true, Duration: 210, ISRC: "USAAA1200001"}
	b, c := a, a
	b.ID = "lmnopqrstuv"
	b.ISRC = ""
	c.ID = "12345678901"
	c.ISRC = "USAAA1200002"
	_, err := selectSpotify(context.Background(), tr, []Candidate{a, b, c}, func(c Candidate) (Candidate, error) { return c, nil })
	var m *MatchError
	if !errors.As(err, &m) || m.Status != MatchUncertain {
		t.Fatal(err)
	}
}

func TestProgressiveDiscoveryPreservesInfrastructureFailure(t *testing.T) {
	tr := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	c := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Duration: 210}
	blocked := errors.New("provider blocked")
	searches := 0
	_, err := discoverSpotify(context.Background(), tr, DefaultMatchPolicy(), "query", func(string, string) ([]*Candidate, error) { searches++; return []*Candidate{&c}, nil }, func(Candidate) (Candidate, error) { return Candidate{}, blocked })
	if !errors.Is(err, blocked) || searches != 2 {
		t.Fatal(err, searches)
	}
}

func TestEquivalentUploadsDoNotBypassVersionChecks(t *testing.T) {
	tr := Track{Title: "Song", Artist: "Artist", Album: "Album", Duration: 210 * time.Second, ISRC: "USAAA1200001"}
	a := Candidate{ID: "abcdefghijk", Title: "Song", Track: "Song", Artists: []string{"Artist"}, Album: "Album", Channel: "Artist", Verified: true, Duration: 210, ISRC: tr.ISRC}
	for _, version := range []string{"Live", "Remix", "Cover", "Remaster"} {
		t.Run(version, func(t *testing.T) {
			b := a
			b.ID = "lmnopqrstuv"
			b.Title += " " + version
			b.Track = b.Title
			if d := CompareSpotify(tr, b, DefaultMatchPolicy()); d.Status != MatchRejected {
				t.Fatal(d)
			}
			got, err := selectSpotify(context.Background(), tr, []Candidate{a, b}, func(c Candidate) (Candidate, error) { return c, nil })
			if err != nil || got.ID != a.ID {
				t.Fatal(got, err)
			}
		})
	}
}

func TestProgressiveVerificationBudget(t *testing.T) {
	tr := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	calls, checks := 0, 0
	_, err := discoverSpotify(context.Background(), tr, DefaultMatchPolicy(), "query", func(string, string) ([]*Candidate, error) {
		calls++
		var pool []*Candidate
		// Each later pool outranks previous summaries, forcing fresh verification.
		for i := 0; i < 4; i++ {
			c := &Candidate{ID: fmt.Sprintf("video%06d", calls*10+i), Title: "Song", Channel: "Artist", Duration: 200}
			if calls >= 3 {
				c.Duration = 210
			}
			if calls == 4 {
				c.Verified = true
			}
			pool = append(pool, c)
		}
		return pool, nil
	}, func(c Candidate) (Candidate, error) { checks++; c.Duration = 400; return c, nil })
	if err == nil || checks != 8 || calls != 4 {
		t.Fatal(err, checks, calls)
	}
}
