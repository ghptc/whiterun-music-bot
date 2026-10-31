package media

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MappingStore interface {
	LoadMapping(context.Context, string) (Candidate, bool, error)
	SaveMapping(context.Context, string, Candidate) error
	DeleteMapping(context.Context, string) error
}

func mappingKey(t Track, p MatchPolicy) string {
	t.Position, t.Total = 0, 0
	t.ResumeOffset = 0
	t.RequestedBy = ""
	t.SpotifyArtworkURL, t.YouTubeThumbnailURL = "", ""
	t.Timing = nil
	t.VideoID, t.URL, t.Channel = "", "", ""
	t.MatchStatus, t.MatchReason = "", ""
	b, _ := json.Marshal(struct {
		Version int
		Track   Track
		Policy  MatchPolicy
	}{4, t, p})
	if t.SpotifyID != "" {
		return fmt.Sprintf("spotify:%s:%x", t.SpotifyID, sha256.Sum256(b))
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Availability is leased for ten minutes after full extraction. Stream format
// extraction still checks actual access; signed URLs are never cached.
const identityLease = searchCacheTTL

// Reserve the resolver/startup budget before proof expiry.
const VerificationPrefetchHorizon = identityLease - 2*time.Minute

func freshIdentity(c Candidate, now time.Time) bool {
	return !c.CheckedAt.IsZero() && !now.Before(c.CheckedAt) && now.Sub(c.CheckedAt) < identityLease
}

var errVideoIdentityMismatch = errors.New("YouTube returned a different video ID")

// rawVideo shares only extraction, never a caller's musical acceptance. The
// context is the immutable binary/metadata configuration of this Bot instance.
func (r Resolver) rawVideo(ctx context.Context, id, purpose string) (Candidate, error) {
	if !videoID.MatchString(id) {
		return Candidate{}, ErrSpotifyMatch
	}
	contextHash := sha256.Sum256([]byte(r.Binary + "\x00" + strings.Join(MetadataArgs(), "\x00")))
	key := fmt.Sprintf("video-proof:%x:%s", contextHash, id)
	track, err := r.Cache.resolveValid(ctx, key, func(workCtx context.Context) (Track, error) {
		bounded, cancel := context.WithTimeout(workCtx, 45*time.Second)
		defer cancel()
		full, err := r.extract(bounded, "https://www.youtube.com/watch?v="+id, purpose, false)
		if err != nil {
			return Track{}, err
		}
		if full.ID != id {
			return Track{}, errVideoIdentityMismatch
		}
		return Track{VideoID: id, identityProof: &full.Candidate}, nil
	}, func(t Track) bool { return t.identityProof != nil && proofCovers(ctx, *t.identityProof) })
	if err != nil {
		return Candidate{}, err
	}
	if track.identityProof == nil {
		return Candidate{}, ErrSpotifyMatch
	}
	return *track.identityProof, nil
}

// VerifiedSpotify renews the exact recording proof and returns caller-owned
// metadata. A failed comparison never becomes a successful Spotify cache entry.
func (r Resolver) VerifiedSpotify(ctx context.Context, t Track) (Track, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	direct, ok, err := YouTubeURL(t.URL)
	if err != nil || !ok || direct != "https://www.youtube.com/watch?v="+t.VideoID {
		return Track{}, ErrSpotifyMatch
	}
	if err := ctx.Err(); err != nil {
		return Track{}, err
	}
	if proof := t.identityProof; proof != nil && proof.ID == t.VideoID && proofCovers(ctx, *proof) && CompareSpotify(t, *proof, r.matchPolicy()).Status == MatchConfirmed {
		TimingFrom(ctx).Event("spotify_playback_identity_reused", time.Now(), "spotify_track_id", t.SpotifyID, "video_id", t.VideoID)
		return CloneTrack(t), nil
	}
	// A restored queue loses its in-memory proof, not the persisted identity.
	if r.Mappings != nil {
		bounded, stop := catalogContext(ctx)
		cached, hit, lookupErr := r.Mappings.LoadMapping(bounded, MappingKey(t, r.Policy))
		stop()
		if lookupErr == nil && hit && cached.ID == t.VideoID && proofCovers(ctx, cached) && CompareSpotify(t, cached, r.matchPolicy()).Status == MatchConfirmed {
			t.identityProof = &cached
			return CloneTrack(t), nil
		}
	}
	full, err := r.rawVideo(ctx, t.VideoID, "spotify_playback_verification")
	if err != nil {
		return Track{}, err
	}
	decision := CompareSpotify(t, full, r.matchPolicy())
	if decision.Status != MatchConfirmed {
		return Track{}, &MatchError{Status: decision.Status, Reason: decision.Reason}
	}
	t = CloneTrack(t)
	t.identityProof = &full
	if r.Mappings != nil {
		bounded, cancel := catalogContext(ctx)
		err := r.Mappings.SaveMapping(bounded, MappingKey(t, r.Policy), full)
		cancel()
		TimingFrom(ctx).Event("spotify_proof_renewed", time.Now(), "success", err == nil, "video_id", t.VideoID)
	}
	return t, nil
}

// VerifySpotify retains the existing validation contract for library callers.
func (r Resolver) VerifySpotify(ctx context.Context, t Track) error {
	_, err := r.VerifiedSpotify(ctx, t)
	return err
}

func MappingKey(t Track, p *MatchPolicy) string {
	policy := DefaultMatchPolicy()
	if p != nil {
		policy = *p
	}
	return mappingKey(t, policy)
}

// InvalidateSpotify drops both metadata cache layers after playback failure.
// Cleanup ignores a track cancellation, but is bounded and cannot mask playback.
func (r Resolver) InvalidateSpotify(ctx context.Context, t Track) {
	if t.RequestedSource != "spotify" {
		return
	}
	key := MappingKey(t, r.Policy)
	r.Cache.invalidate("spotify:" + key)
	if r.Mappings != nil {
		bounded, cancel := catalogContext(context.WithoutCancel(ctx))
		defer cancel()
		err := deleteMappingVideo(bounded, r.Mappings, key, t.VideoID)
		TimingFrom(ctx).Event("spotify_mapping_invalidated", time.Now(), "success", err == nil)
	}
}

func deleteMappingVideo(ctx context.Context, store MappingStore, key, id string) error {
	if conditional, ok := store.(interface {
		DeleteMappingForVideo(context.Context, string, string) error
	}); ok {
		return conditional.DeleteMappingForVideo(ctx, key, id)
	}
	return store.DeleteMapping(ctx, key)
}

type expectedPlaybackKey struct{}

// WithPlaybackEstimate is only a freshness hint; it never authorizes matching.
func WithPlaybackEstimate(ctx context.Context, eta time.Duration) context.Context {
	until := time.Now()
	if eta > 0 {
		until = until.Add(eta + time.Minute)
	}
	return context.WithValue(ctx, expectedPlaybackKey{}, until)
}
func proofCovers(ctx context.Context, c Candidate) bool {
	if !freshIdentity(c, time.Now()) {
		return false
	}
	until, _ := ctx.Value(expectedPlaybackKey{}).(time.Time)
	return until.IsZero() || until.Before(c.CheckedAt.Add(identityLease))
}
