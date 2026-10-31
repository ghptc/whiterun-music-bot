package media

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRemasterEditionRecovery(t *testing.T) {
	track := Track{Title: "Nothing Else Matters - Remastered 2021", Artist: "Metallica", Duration: 389 * time.Second}
	summary := Candidate{ID: "ozXZnwYTMbs", Title: "Nothing Else Matters (Remastered)", Channel: "Metallica", Verified: true, Duration: 389}
	if got := rankSpotify(track, summary, true); got.Reason != "accepted" {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		name  string
		year  int
		title string
		want  MatchStatus
	}{
		{"structured edition", 2021, summary.Title, MatchConfirmed},
		{"unknown edition", 0, summary.Title, MatchUncertain},
		{"different edition", 2009, summary.Title, MatchRejected},
		{"explicit conflict", 2021, "Nothing Else Matters (Remastered 2009)", MatchRejected},
		{"cover", 2021, "Nothing Else Matters (Remastered) Cover", MatchRejected},
		{"live", 2021, "Nothing Else Matters (Remastered) Live", MatchRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := summary
			c.ReleaseYear = tc.year
			c.Title = tc.title
			if d := CompareSpotify(track, c, DefaultMatchPolicy()); d.Status != tc.want {
				t.Fatal(d)
			}
		})
	}
	untrusted := summary
	untrusted.Channel = "Unrelated uploader"
	untrusted.Verified = false
	untrusted.Title = "Metallica - Nothing Else Matters (Remastered 2021)"
	untrusted.ReleaseYear = 2021
	if d := CompareSpotify(track, untrusted, DefaultMatchPolicy()); d.Status != MatchUncertain || d.Reason != "recording_provenance_unknown" {
		t.Fatal(d)
	}
	calls := 0
	full := summary
	full.ReleaseYear = 2021
	got, err := selectSpotify(context.Background(), track, []Candidate{summary}, func(c Candidate) (Candidate, error) { calls++; return full, nil })
	if err != nil || got.ID != full.ID || calls != 1 {
		t.Fatal(got, err, calls)
	}
	// Persisted evidence must survive JSON/cache use and satisfy playback checks.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var restored Candidate
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	got = restored
	if d := CompareSpotify(track, got, DefaultMatchPolicy()); d.Status != MatchConfirmed {
		t.Fatal(d)
	}
}
