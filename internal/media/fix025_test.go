package media

import (
	"testing"
	"time"
)

func TestFIX025ExactRemixerCredit(t *testing.T) {
	for _, tc := range []struct {
		title, artist, remixer string
		credits                []string
		seconds                int
	}{
		{"Pursuit Of Happiness - Extended Steve Aoki Remix", "Kid Cudi", "Steve Aoki", []string{"Kid Cudi", "MGMT", "Ratatat"}, 374},
		{"Heads Will Roll - A-Trak Remix", "Yeah Yeah Yeahs", "A-Trak", []string{"Yeah Yeah Yeahs"}, 384},
	} {
		tr := Track{Title: tc.title, Artist: tc.artist, Artists: append(append([]string{}, tc.credits...), tc.remixer), Duration: time.Duration(tc.seconds) * time.Second}
		c := Candidate{ID: "abcdefghijk", Title: tc.title, Track: tc.title, Artists: tc.credits, Duration: float64(tc.seconds), Channel: tc.artist + " - Topic"}
		if got := rankSpotify(tr, c, false); got.Reason != "accepted" {
			t.Fatal(tc.title, got)
		}
		c.Track = "Other remix"
		c.Title = "Other remix"
		if got := rankSpotify(tr, c, false); got.Reason == "accepted" {
			t.Fatal("wrong remix accepted")
		}
		c.Track = tc.title
		c.Title = tc.title
		tr.Artists = append(tr.Artists, "Uncredited singer")
		if got := rankSpotify(tr, c, false); got.Reason == "accepted" {
			t.Fatal("unrelated credit accepted")
		}
	}
}
