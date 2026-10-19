package media

import (
	"fmt"
	"strings"
	"time"
)

type MatchStatus string

const (
	MatchConfirmed MatchStatus = "MATCH_CONFIRMED"
	MatchUncertain MatchStatus = "MATCH_UNCERTAIN"
	MatchRejected  MatchStatus = "MATCH_REJECTED"
)

// MatchPolicy weights are ranking units, never acoustic probabilities.
type MatchPolicy struct {
	MinTitlePrecision   float64
	MaxDurationDelta    time.Duration
	MaxDurationFraction float64
	MinScore            int
	MinScoreMargin      int // Legacy configuration; never gates a confirmed candidate.
}

func DefaultMatchPolicy() MatchPolicy {
	return MatchPolicy{MinTitlePrecision: .65, MaxDurationDelta: 15 * time.Second, MaxDurationFraction: .08, MinScore: 650, MinScoreMargin: 20}
}
func (p MatchPolicy) Validate() error {
	if p.MinTitlePrecision <= 0 || p.MinTitlePrecision > 1 || p.MaxDurationDelta < 0 || p.MaxDurationFraction < 0 || p.MaxDurationFraction > .25 || p.MinScore < 650 || p.MinScoreMargin < 1 {
		return fmt.Errorf("invalid Spotify match policy")
	}
	return nil
}
func (r Resolver) matchPolicy() MatchPolicy {
	if r.Policy != nil {
		return *r.Policy
	}
	return DefaultMatchPolicy()
}

type MatchError struct {
	Status MatchStatus
	Reason string
}

func (e *MatchError) Error() string {
	return fmt.Sprintf("%s (%s: %s)", ErrSpotifyMatch, e.Status, e.Reason)
}
func (e *MatchError) Unwrap() error { return ErrSpotifyMatch }

type MatchDecision struct {
	Status MatchStatus
	Reason string
	Score  int
}

// CompareSpotify evaluates full metadata, independently of search order or popularity.
func CompareSpotify(t Track, c Candidate, p MatchPolicy) MatchDecision {
	if err := p.Validate(); err != nil {
		return MatchDecision{Status: MatchRejected, Reason: "invalid_policy"}
	}
	b := rankSpotifyPolicy(t, c, false, p)
	status := MatchRejected
	if b.Reason == "accepted" {
		// Independent provenance is required in addition to mandatory identity
		// checks. A title, score or artist-looking channel alone is plausible.
		class, _ := sourceClass(t, c)
		sharedISRC := validISRC.MatchString(strings.ToUpper(t.ISRC)) && strings.EqualFold(t.ISRC, c.ISRC)
		if sharedISRC || class == "verified_artist" || class == "topic" || class == "artist_vevo" || class == "release" {
			status = MatchConfirmed
			b.Reason = class + "_and_identity"
			if sharedISRC {
				b.Reason = "matching_isrc_and_identity"
			}
		} else {
			status = MatchUncertain
			b.Reason = "recording_provenance_unknown"
		}
	} else if b.Reason == "duration_unknown" || b.Reason == "credits_unknown" || b.Reason == "primary_artist_mismatch" && c.Artist == "" && len(c.Artists) == 0 && c.Channel == "" && c.Uploader == "" {
		status = MatchUncertain
	}
	return MatchDecision{Status: status, Reason: b.Reason, Score: b.Score}
}
