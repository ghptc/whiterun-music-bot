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

func TestFidelityDoesNotInferSimilarArtistOrMissingGuest(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Artists: []string{"Artist"}, Duration: 210 * time.Second}
	for _, c := range []Candidate{
		{ID: "abcdefghijk", Title: "Song", Channel: "Artist Junior", Duration: 210},
		{ID: "abcdefghijk", Title: "Artist Junior - Song", Channel: "Archive", Duration: 210},
	} {
		if d := CompareSpotify(track, c, DefaultMatchPolicy()); d.Status == MatchConfirmed {
			t.Fatal(d, c)
		}
	}
	track.Artists = append(track.Artists, "Guest")
	c := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Duration: 210}
	if d := CompareSpotify(track, c, DefaultMatchPolicy()); d.Status == MatchConfirmed {
		t.Fatal("missing collaborator", d)
	}
}
func TestSpotifyCacheIdentityCannotBeMutated(t *testing.T) {
	cache := &SearchCache{}
	original := Track{Artists: []string{"Artist"}, Explicit: new(true)}
	cache.put("key", original, time.Now())
	original.Artists[0] = "outside"
	*original.Explicit = false
	got, _ := cache.get("key", time.Now())
	if got.Artists[0] != "Artist" || !*got.Explicit {
		t.Fatal(got)
	}
	got.Artists[0] = "reader"
	*got.Explicit = false
	again, _ := cache.get("key", time.Now())
	if again.Artists[0] != "Artist" || !*again.Explicit {
		t.Fatal(again)
	}
}

func TestIncompleteVerifiedMetadataIsUncertain(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	c := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist"}
	_, err := selectSpotify(context.Background(), track, []Candidate{c}, func(c Candidate) (Candidate, error) { return c, nil })
	var decision *MatchError
	if !errors.As(err, &decision) || decision.Status != MatchUncertain {
		t.Fatal(err)
	}
}

func TestStructuredSpotifyIdentityConstrainsDirectResolver(t *testing.T) {
	r, _ := resolverFixture(t, `{"id":"abcdefghijk","title":"Song Live","channel":"Artist","duration":210}`)
	r.Match = &Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	if _, err := r.Resolve(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk"); !errors.Is(err, ErrSpotifyMatch) {
		t.Fatal("direct path bypassed identity", err)
	}
}

func TestCandidateVerificationFailureCannotBecomeARejectionOrFallback(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	a := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist", Duration: 210}
	b := a
	b.ID = "lmnopqrstuv"
	unavailable := errors.New("Sign in to confirm you're not a bot")
	_, err := selectSpotify(context.Background(), track, []Candidate{a, b}, func(c Candidate) (Candidate, error) {
		if c.ID == a.ID {
			return Candidate{}, unavailable
		}
		return c, nil
	})
	var decision *MatchError
	if !errors.Is(err, unavailable) || errors.As(err, &decision) {
		t.Fatal(err)
	}
}

func TestUnstructuredGuestNamesDoNotConfirmSimilarCollaborators(t *testing.T) {
	track := Track{Title: "Song (feat. Guest)", Artist: "Artist", Artists: []string{"Artist", "Guest"}, Duration: 210 * time.Second}
	for _, title := range []string{"Artist - Song feat Guest", "Artist - Song feat Guest Junior"} {
		c := Candidate{ID: "abcdefghijk", Title: title, Channel: "Artist", Duration: 210}
		if d := CompareSpotify(track, c, DefaultMatchPolicy()); d.Status == MatchConfirmed {
			t.Fatal("unverified collaborator", d)
		}
	}
}
