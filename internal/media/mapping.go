package media

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

type MappingStore interface {
	LoadMapping(context.Context, string) (Candidate, bool, error)
	SaveMapping(context.Context, string, Candidate) error
	DeleteMapping(context.Context, string) error
}

func mappingKey(t Track, p MatchPolicy) string {
	t.Position, t.Total = 0, 0
	t.RequestedBy = ""
	t.Timing = nil
	t.VideoID, t.URL, t.Channel = "", "", ""
	t.MatchStatus, t.MatchReason = "", ""
	b, _ := json.Marshal(struct {
		Version int
		Track   Track
		Policy  MatchPolicy
	}{2, t, p})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// VerifySpotify fetches fresh metadata for the exact video; it never searches a
// substitute or changes the requested identity. Playback must fail closed.
func (r Resolver) VerifySpotify(ctx context.Context, t Track) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	direct, ok, err := YouTubeURL(t.URL)
	if err != nil || !ok || direct != "https://www.youtube.com/watch?v="+t.VideoID {
		return ErrSpotifyMatch
	}
	full, err := r.extract(ctx, direct, "spotify_playback_verification", false)
	if err != nil {
		return err
	}
	if full.ID != t.VideoID {
		return ErrSpotifyMatch
	}
	d := CompareSpotify(t, full.Candidate, r.matchPolicy())
	if d.Status != MatchConfirmed {
		return &MatchError{Status: d.Status, Reason: d.Reason}
	}
	return nil
}

func MappingKey(t Track, p *MatchPolicy) string {
	policy := DefaultMatchPolicy()
	if p != nil {
		policy = *p
	}
	return mappingKey(t, policy)
}
