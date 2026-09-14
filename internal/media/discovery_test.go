package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
)

func flat(c Candidate) Candidate { c.Type = "url"; c.Categories = nil; return c }

func TestDiscoveryVerifiesUncertainMusic(t *testing.T) {
	c := flat(song("Arctic Monkeys 505 live", "Concert archive"))
	calls := 0
	got, err := selectDiscovered(context.Background(), "Arctic Monkeys 505 live", []Candidate{c}, func(candidate Candidate) (Candidate, error) {
		calls++
		if IsMusic(candidate) {
			t.Fatal("fixture must require music evidence")
		}
		candidate.Categories = []string{"Music"}
		return candidate, nil
	})
	if err != nil || got.Title != c.Title || calls != 1 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
}

func TestDiscoveryRejectsNonMusicBeforeVerification(t *testing.T) {
	for _, term := range []string{"podcast", "interview", "reaction", "review", "tutorial", "documentary", "gameplay", "news"} {
		c := flat(song("Arctic Monkeys 505 "+term, "Channel"))
		_, err := selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{c}, func(Candidate) (Candidate, error) { t.Fatal("extracted obvious non-music"); return Candidate{}, nil })
		if !errors.Is(err, ErrNoSong) {
			t.Fatalf("accepted %s: %v", term, err)
		}
	}
}

func TestDiscoveryDoesNotTreatMissingCategoryAsMusic(t *testing.T) {
	c := flat(song("Arctic Monkeys 505", "Channel"))
	_, err := selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{c}, func(c Candidate) (Candidate, error) { return c, nil })
	if !errors.Is(err, ErrNoSong) {
		t.Fatalf("accepted unverified content: %v", err)
	}
}

func TestDiscoveryPrefersCanonicalAndRequestedVariants(t *testing.T) {
	for _, variant := range []string{"live", "unplugged", "remix", "acoustic"} {
		t.Run(variant, func(t *testing.T) {
			normal := flat(song("Arctic Monkeys 505 Official Audio", "Arctic Monkeys"))
			version := flat(song("Arctic Monkeys 505 "+variant, "Concert archive"))
			version.ID = "lmnopqrstuv"
			verify := func(c Candidate) (Candidate, error) { c.Categories = []string{"Music"}; return c, nil }
			got, err := selectDiscovered(context.Background(), "Arctic Monkeys 505 "+variant, []Candidate{normal, version}, verify)
			if err != nil || got.ID != version.ID {
				t.Fatalf("requested variant: %+v %v", got, err)
			}
			got, err = selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{version, normal}, func(Candidate) (Candidate, error) {
				t.Fatal("canonical flat result already establishes music")
				return Candidate{}, nil
			})
			if err != nil || got.ID != normal.ID {
				t.Fatalf("canonical: %+v %v", got, err)
			}
		})
	}
}

func TestDiscoveryReRanksUnconfirmedOfficialChannel(t *testing.T) {
	generic := flat(song("Arctic Monkeys 505", "Arctic Monkeys Archive"))
	generic.Verified = true
	official := flat(song("Arctic Monkeys 505 Official Audio", "Domino"))
	official.ID = "lmnopqrstuv"
	calls := 0
	got, err := selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{generic, official}, func(c Candidate) (Candidate, error) {
		calls++
		c.Categories = []string{"Music"}
		return c, nil
	})
	if err != nil || got.ID != official.ID || calls != 0 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
	// Complete artist metadata preserves the existing higher tier.
	generic.Categories = []string{"Music"}
	generic.Artist = "Arctic Monkeys"
	got, err = selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{generic, official}, func(c Candidate) (Candidate, error) {
		c.Categories = []string{"Music"}
		c.Artist = "Arctic Monkeys"
		return c, nil
	})
	if err != nil || got.ID != generic.ID {
		t.Fatalf("lost official artist: %+v %v", got, err)
	}
}

func TestDiscoveryTriesNextCandidateAfterVerificationFailure(t *testing.T) {
	first := flat(song("Arctic Monkeys 505 live", "Archive"))
	second := first
	second.ID = "lmnopqrstuv"
	for _, fail := range []bool{false, true} {
		calls := 0
		got, err := selectDiscovered(context.Background(), "Arctic Monkeys 505 live", []Candidate{first, second}, func(c Candidate) (Candidate, error) {
			calls++
			if c.ID == first.ID {
				if fail {
					return Candidate{}, errors.New("extractor failure")
				}
				c.Title += " interview"
				return c, nil
			}
			c.Categories = []string{"Music"}
			return c, nil
		})
		if err != nil || got.ID != second.ID || calls != 2 {
			t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
		}
	}
}

func TestDiscoveryDoesNotPromoteUnrelatedVerifiedChannel(t *testing.T) {
	first := flat(song("Arctic Monkeys 505", "Archive"))
	unrelated := flat(song("Arctic Monkeys 505", "Pizza Music"))
	unrelated.ID = "lmnopqrstuv"
	unrelated.Verified = true
	calls := 0
	got, err := selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{first, unrelated}, func(c Candidate) (Candidate, error) {
		calls++
		c.Categories = []string{"Music"}
		return c, nil
	})
	if err != nil || got.ID != first.ID || calls != 1 {
		t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
	}
}

func TestDiscoveryWithCompleteMetadataMatchesExistingSelection(t *testing.T) {
	candidates := []Candidate{
		song("Arctic Monkeys 505 live", "Archive"),
		song("Arctic Monkeys 505 Official Audio", "Artist"),
		song("Arctic Monkeys 505 remix", "DJ"),
		song("Arctic Monkeys 505 acoustic", "Singer"),
	}
	for _, query := range []string{"Arctic Monkeys 505", "Arctic Monkeys 505 live", "Arctic Monkeys 505 remix", "Arctic Monkeys 505 acoustic"} {
		want, ok := Select(query, candidates)
		if !ok {
			t.Fatal("fixture has no match")
		}
		got, err := selectDiscovered(context.Background(), query, candidates, func(Candidate) (Candidate, error) { t.Fatal("complete metadata re-extracted"); return Candidate{}, nil })
		if err != nil || got.Title != want.Title {
			t.Fatalf("%s: got=%+v want=%+v err=%v", query, got, want, err)
		}
	}
}

func TestDiscoveryPreservesVerificationError(t *testing.T) {
	want := errors.New("YouTube timed out")
	_, err := selectDiscovered(context.Background(), "Arctic Monkeys 505", []Candidate{flat(song("Arctic Monkeys 505", "Archive"))}, func(Candidate) (Candidate, error) { return Candidate{}, want })
	if !errors.Is(err, want) || errors.Is(err, ErrNoSong) {
		t.Fatalf("lost extraction failure: %v", err)
	}
}

// Captured YouTube metadata is a fixture, never a live test dependency. Each
// full set contains exactly the same IDs/order as its flat discovery set.
func TestCapturedDiscoveryPreservesSelection(t *testing.T) {
	data, err := os.ReadFile("testdata/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Query      string
		Flat, Full []Candidate
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("fixture cases=%d", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.Query, func(t *testing.T) {
			if len(tt.Flat) != 10 || len(tt.Full) != 10 {
				t.Fatal("fixture must contain all ten candidates")
			}
			full := make(map[string]Candidate)
			for i, c := range tt.Full {
				if tt.Flat[i].ID != c.ID {
					t.Fatal("candidate sets/order differ")
				}
				full[c.ID] = c
			}
			want, ok := Select(tt.Query, tt.Full)
			if !ok {
				t.Fatal("baseline has no song")
			}
			calls := 0
			got, err := selectDiscovered(context.Background(), tt.Query, tt.Flat, func(c Candidate) (Candidate, error) { calls++; return full[c.ID], nil })
			if err != nil || got.ID != want.ID {
				t.Fatalf("got=%+v want=%+v error=%v", got, want, err)
			}
			if calls > 1 {
				t.Fatalf("extracted %d candidates", calls)
			}
		})
	}
}

func TestDiscoveryRegressionQueries(t *testing.T) {
	for _, tt := range []struct{ query, title string }{
		{"The Strokes Sunday", "The Strokes - Why Are Sundays So Depressing?"},
		{"Metallica unforguiven", "Metallica - The Unforgiven"},
		{"Metallica inforguiven", "Metallica - The Unforgiven"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			c := flat(song(tt.title, ""))
			state, reason := ClassifyDiscovery(tt.query, c)
			if state != Uncertain {
				t.Fatalf("state=%s reason=%s", state, reason)
			}
			calls := 0
			got, err := selectDiscovered(context.Background(), tt.query, []Candidate{c}, func(c Candidate) (Candidate, error) {
				calls++
				c.Categories = []string{"Music"}
				return c, nil
			})
			if err != nil || got.Title != tt.title || calls != 1 {
				t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
			}
			c.Title += " (Official Video)"
			state, _ = ClassifyDiscovery(tt.query, c)
			if state != Eligible {
				t.Fatalf("official video state=%s", state)
			}
		})
	}
}

func TestDiscoveryStates(t *testing.T) {
	c := song("Metallica - The Unforgiven", "Metallica")
	if state, _ := ClassifyDiscovery("Metallica unforguiven", c); state != Eligible {
		t.Fatal(state)
	}
	c = flat(c)
	c.Duration = 0
	c.Channel = ""
	if state, _ := ClassifyDiscovery("Metallica unforguiven", c); state != Uncertain {
		t.Fatal(state)
	}
	for _, term := range []string{"podcast", "interview", "reaction", "review", "tutorial", "documentary", "gameplay", "news"} {
		negative := c
		negative.Title += " " + term
		if state, reason := ClassifyDiscovery("Metallica unforguiven", negative); state != Rejected || reason != "hard_reject="+term {
			t.Fatalf("%s %s", state, reason)
		}
	}
}

func TestDiscoveryVerificationBudget(t *testing.T) {
	candidates := make([]Candidate, 10)
	for i := range candidates {
		candidates[i] = flat(song("Metallica - The Unforgiven", ""))
		candidates[i].ID = fmt.Sprintf("video%06d", i)
	}
	for _, winner := range []int{2, 3, -1} {
		calls := 0
		got, err := selectDiscovered(context.Background(), "Metallica unforguiven", candidates, func(c Candidate) (Candidate, error) {
			if c.ID != candidates[calls].ID {
				t.Fatal("search order not preserved")
			}
			if calls == winner {
				c.Categories = []string{"Music"}
			}
			calls++
			return c, nil
		})
		if calls != 3 {
			t.Fatalf("calls=%d", calls)
		}
		if winner == 2 {
			if err != nil || got.ID != candidates[2].ID {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		} else if !errors.Is(err, ErrNoSong) {
			t.Fatalf("budget exceeded: %v", err)
		}
	}
}

func TestLiveResolverTiming(t *testing.T) {
	binary := os.Getenv("BARD_LIVE_YTDLP")
	if binary == "" {
		t.Skip("opt-in network timing probe")
	}
	for _, query := range []string{"Arctic Monkeys 505", "the strokes sunday", "metallica unforguiven", "metallica inforguiven"} {
		t.Run(query, func(t *testing.T) {
			ctx := WithTiming(context.Background(), NewTiming(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))))
			got, err := (Resolver{Binary: binary}).Resolve(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("selected %s", got.Title)
		})
	}
}
