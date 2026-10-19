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
	}{3, t, p})
	if t.SpotifyID != "" {
		return fmt.Sprintf("spotify:%s:%x", t.SpotifyID, sha256.Sum256(b))
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Availability is leased for ten minutes after full extraction. Stream format
// extraction still checks actual access; signed URLs are never cached.
const identityLease = searchCacheTTL

func freshIdentity(c Candidate, now time.Time) bool {
	return !c.CheckedAt.IsZero() && !now.Before(c.CheckedAt) && now.Sub(c.CheckedAt) < identityLease
}

// VerifySpotify reuses a fresh, identity-bound proof or extracts the exact video.
// It never searches a substitute or changes the requested identity.
func (r Resolver) VerifySpotify(ctx context.Context, t Track) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	direct, ok, err := YouTubeURL(t.URL)
	if err != nil || !ok || direct != "https://www.youtube.com/watch?v="+t.VideoID {
		return ErrSpotifyMatch
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if proof := t.identityProof; proof != nil && proof.ID == t.VideoID && freshIdentity(*proof, time.Now()) && CompareSpotify(t, *proof, r.matchPolicy()).Status == MatchConfirmed {
		TimingFrom(ctx).Event("spotify_playback_identity_reused", time.Now(), "spotify_track_id", t.SpotifyID, "video_id", t.VideoID)
		return nil
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
