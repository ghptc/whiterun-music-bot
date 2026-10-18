package media

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFidelityAdversarialDecisions(t *testing.T) {
	original := Track{SpotifyID: "0123456789012345678901", Title: "Déjà Vu", Artist: "Beyoncé", Artists: []string{"Beyoncé"}, Duration: 210 * time.Second, ISRC: "US1234567890", Explicit: new(true)}
	good := Candidate{ID: "abcdefghijk", Title: "Beyonce - Deja Vu", Artist: "Beyonce", Duration: 210}
	for _, tc := range []struct {
		name   string
		mutate func(*Candidate)
		want   MatchStatus
	}{
		{"correct unofficial", func(c *Candidate) { c.Channel = "Archive" }, MatchConfirmed},
		{"official wrong artist", func(c *Candidate) { c.Artist = "Beyonce Tribute"; c.Channel = "Beyonce"; c.Verified = true }, MatchRejected},
		{"same title other artist", func(c *Candidate) { c.Artist = "Other" }, MatchRejected},
		{"similar artist", func(c *Candidate) { c.Artist = "Beyonce Junior" }, MatchRejected},
		{"duration", func(c *Candidate) { c.Duration = 310 }, MatchRejected},
		{"unknown duration", func(c *Candidate) { c.Duration = 0 }, MatchUncertain},
		{"isrc contradiction", func(c *Candidate) { c.ISRC = "XX9999999999" }, MatchRejected},
		{"clean substitution", func(c *Candidate) { c.Explicit = new(false) }, MatchRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.mutate(&c)
			d := CompareSpotify(original, c, DefaultMatchPolicy())
			if d.Status != tc.want {
				t.Fatalf("%+v", d)
			}
		})
	}
	for _, v := range []string{"Cover", "Live", "Remix", "Slowed", "Sped Up", "Instrumental", "Karaoke", "Remastered 2020", "Acoustic"} {
		t.Run(v, func(t *testing.T) {
			c := good
			c.Title += " (" + v + ")"
			c.Verified = true
			if d := CompareSpotify(original, c, DefaultMatchPolicy()); d.Status != MatchRejected {
				t.Fatal(d)
			}
		})
	}
}
func TestFidelityAmbiguityAndAbsence(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	a := Candidate{ID: "abcdefghijk", Title: "Artist - Song", Channel: "Archive", Duration: 210}
	b := a
	b.ID = "lmnopqrstuv"
	for _, candidates := range [][]Candidate{nil, {a, b}, {b, a}} {
		_, err := selectSpotify(context.Background(), track, candidates, func(c Candidate) (Candidate, error) { return c, nil })
		var decision *MatchError
		if !errors.As(err, &decision) {
			t.Fatal(err)
		}
		want := MatchUncertain
		if len(candidates) == 0 {
			want = MatchRejected
		}
		if decision.Status != want {
			t.Fatal(decision)
		}
	}
}
func TestFidelityPolicyAndOriginalMetadata(t *testing.T) {
	p := DefaultMatchPolicy()
	p.MaxDurationDelta = time.Second
	p.MaxDurationFraction = 0
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second, SpotifyID: "id", ISRC: "isrc", Explicit: new(true), Position: 8, Total: 10}
	c := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Duration: 212}
	if CompareSpotify(track, c, p).Status != MatchRejected {
		t.Fatal("ignored duration policy")
	}
	resolver := CollectionResolver{Search: func(context.Context, string) (Track, error) {
		return Track{URL: "https://youtube.com/watch?v=abcdefghijk", Title: "Wrong", Duration: 212 * time.Second}, nil
	}}
	got, err := resolver.MatchSpotify(context.Background(), track, true)
	if err != nil || got.Duration != track.Duration || got.Title != track.Title || got.SpotifyID != track.SpotifyID || got.ISRC != track.ISRC || got.Position != 8 || got.Explicit == nil || !*got.Explicit {
		t.Fatal(got, err)
	}
	p.MinScoreMargin = 0
	if p.Validate() == nil {
		t.Fatal("accepted invalid policy")
	}
}
