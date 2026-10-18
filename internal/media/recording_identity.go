package media

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

var validISRC = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

// recordingEquivalence is corroboration of metadata, never acoustic proof.
// Call only after both candidates passed full Spotify integrity checks.
func recordingEquivalence(t Track, a, b Candidate) string {
	if a.ID == b.ID {
		return "same_video"
	}
	// Unknown duration or a substantial edit cannot establish equivalence.
	if a.Duration <= 0 || b.Duration <= 0 || math.Abs(a.Duration-b.Duration) > 2 {
		return ""
	}
	if a.Explicit != nil && b.Explicit != nil && *a.Explicit != *b.Explicit {
		return ""
	}
	ai, bi := strings.ToUpper(a.ISRC), strings.ToUpper(b.ISRC)
	if ai != "" && bi != "" && ai != bi {
		return ""
	}
	if validISRC.MatchString(ai) && ai == bi {
		return "shared_isrc_duration_version"
	}
	// Remasters without a shared identifier remain distinct even with copied credits.
	if versionPresent(t.Title+" "+t.Album, "remaster") {
		return ""
	}
	authoritative := func(c Candidate) bool {
		class, _ := sourceClass(t, c)
		return class == "verified_artist" || class == "topic" || class == "artist_vevo" || class == "release"
	}
	if !authoritative(a) || !authoritative(b) || t.Album == "" || a.Track == "" || b.Track == "" || len(a.Artists) == 0 || len(b.Artists) == 0 {
		return ""
	}
	if normalized(a.Track) != normalized(b.Track) || normalized(a.Track) != normalized(t.Title) || normalized(a.Album) != normalized(t.Album) || normalized(b.Album) != normalized(t.Album) {
		return ""
	}
	credits := func(c Candidate) []string {
		v := make([]string, len(c.Artists))
		for i, s := range c.Artists {
			v[i] = normalized(s)
		}
		slices.Sort(v)
		return slices.Compact(v)
	}
	if !slices.Equal(credits(a), credits(b)) {
		return ""
	}
	return "authoritative_release_credits_duration_version"
}
