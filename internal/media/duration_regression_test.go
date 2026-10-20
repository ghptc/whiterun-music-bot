package media

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestDefectExtractorDurationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        time.Duration
		rejected    bool
	}{
		{"unknown", "", 0, false},
		{"zero", `,"duration":0`, 0, false},
		{"fractional", `,"duration":0.125`, 125 * time.Millisecond, false},
		{"live unknown", `,"is_live":true`, 0, false},
		{"negative", `,"duration":-0.001`, 0, true},
		{"overflow", `,"duration":1e20`, 0, true},
		{"rounded overflow", `,"duration":9223372036.854776`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := resolverFixture(t, `{"id":"abcdefghijk","title":"Song"`+tc.field+`}`)
			tr, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk")
			if tc.rejected {
				if err == nil || tr.URL != "" {
					t.Fatalf("invalid duration published: %+v %v", tr, err)
				}
				return
			}
			if err != nil || tr.Duration != tc.want {
				t.Fatal(tr, err)
			}
		})
	}
}
func TestDefectPlaylistDoesNotPublishMalformedDuration(t *testing.T) {
	for _, duration := range []string{"-1", "1e20"} {
		t.Run(duration, func(t *testing.T) {
			r, _ := resolverFixture(t, fmt.Sprintf(`{"entries":[{"id":"abcdefghijk","title":"bad","duration":%s},{"id":"lmnopqrstuv","title":"good","duration":2}]}`, duration))
			got, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://www.youtube.com/playlist?list=PLfixture")
			if err != nil || len(got.Tracks) != 1 || got.Tracks[0].VideoID != "lmnopqrstuv" || got.Skipped != 1 {
				t.Fatalf("invalid playlist entry escaped: %+v %v", got, err)
			}
		})
	}
}

// NaN and Infinity cannot occur in valid JSON, but cached/constructed candidates
// still must not be confirmed as Spotify recordings.
func TestDefectFidelityRejectsInvalidDuration(t *testing.T) {
	for _, duration := range []float64{-1, math.NaN(), math.Inf(1), 1e20} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			tr, c := qualityIdentity()
			c.Duration = duration
			if got := CompareSpotify(tr, c, DefaultMatchPolicy()); got.Status != MatchRejected {
				t.Fatal(got)
			}
		})
	}
}

func FuzzAdvancedDurationValidity(f *testing.F) {
	for _, value := range []float64{-1, 0, 210, 1e20, math.NaN(), math.Inf(1)} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, seconds float64) {
		tr, c := qualityIdentity()
		c.Duration = seconds
		decision := CompareSpotify(tr, c, DefaultMatchPolicy())
		if decision.Status == MatchConfirmed {
			if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || math.Abs(seconds-210) > 16.8 {
				t.Fatalf("impossible or divergent recording confirmed: seconds=%v decision=%+v", seconds, decision)
			}
		}
		if seconds == 210 && decision.Status != MatchConfirmed {
			t.Fatal("exact recording refused", decision)
		}
	})
}
