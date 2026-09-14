package media

import (
	"context"
	"time"
)

type MusicState string

const (
	Eligible                  MusicState = "eligible"
	Uncertain                 MusicState = "uncertain"
	Rejected                  MusicState = "rejected"
	maxDiscoveryVerifications            = 3
)

// ClassifyDiscovery distinguishes absent evidence from explicit negatives.
func ClassifyDiscovery(query string, c Candidate) (MusicState, string) {
	if !videoID.MatchString(c.ID) {
		return Rejected, "invalid_video_id"
	}
	for _, term := range []string{"podcast", "interview", "reaction", "reacts", "review", "tutorial", "documentary", "gameplay", "news", "shorts", "short"} {
		if has(c.Title+" "+c.Channel+" "+c.Uploader, term) {
			return Rejected, "hard_reject=" + term
		}
	}
	shape := c
	if shape.Duration == 0 {
		shape.Duration = 45
	}
	if shape.Title == "" {
		shape.Title = "unknown"
	}
	if !musicShape(shape) {
		return Rejected, "invalid_music_shape"
	}
	if c.Title == "" {
		return Uncertain, "missing_flat_metadata"
	}
	if _, ok := scoreMusic(query, c); !ok {
		return Rejected, "query_match_too_low"
	}
	if IsMusic(c) {
		return Eligible, "music_evidence"
	}
	if c.Duration == 0 || len(c.Categories) == 0 || c.Channel == "" {
		return Uncertain, "missing_flat_metadata"
	}
	return Uncertain, "insufficient_music_evidence"
}

// Discovery ranks the ten search summaries, retaining uncertain music results
// for verification instead of either accepting them or losing concert uploads.
// Verification alone is not proof of an official artist channel: that bonus is
// provisional until the selected video's artist metadata confirms it.
func selectDiscovered(ctx context.Context, query string, candidates []Candidate, verify func(Candidate) (Candidate, error)) (Candidate, error) {
	type ranked struct {
		candidate Candidate
		score     int
		verify    bool
	}
	timing := TimingFrom(ctx)
	start := time.Now()
	var remaining []ranked
	counts := map[MusicState]int{}
	for _, c := range candidates {
		state, reason := ClassifyDiscovery(query, c)
		counts[state]++
		if timing != nil {
			timing.log.DebugContext(ctx, "discovery_candidate", "video_id", c.ID, "title", c.Title, "state", state, "reason", reason)
		}
		if state == Rejected {
			continue
		}
		provisional := c
		uncertainArtist := state == Uncertain && c.Verified && c.Artist == "" && len(c.Categories) == 0 && channelMatchesQuery(query, c)
		if uncertainArtist {
			provisional.Artist = c.Channel
		}
		score, ok := scoreMusic(query, provisional)
		if !ok {
			score = 0
		}
		remaining = append(remaining, ranked{c, score, !IsMusic(c) || uncertainArtist})
	}
	timing.Event("discovery_ranking_complete", start, "candidates", len(candidates), "eligible", counts[Eligible], "uncertain", counts[Uncertain], "rejected", counts[Rejected])
	verifications := 0
	defer func() { timing.Event("discovery_complete", start, "full_verifications", verifications) }()
	// Prefer a strong confirmed match before spending subprocesses on metadata
	// that could only improve a provisional channel bonus. Requested versions
	// can still outrank a canonical result whose version penalty lowers its score.
	bestEligible := -1
	for i, c := range remaining {
		if !c.verify && c.score >= 300 && (bestEligible < 0 || c.score > remaining[bestEligible].score) {
			bestEligible = i
		}
	}
	if bestEligible >= 0 {
		competitive := false
		for _, c := range remaining {
			if c.score > remaining[bestEligible].score+30 {
				competitive = true
			}
		}
		if !competitive {
			return remaining[bestEligible].candidate, nil
		}
	}
	var lastErr error
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return Candidate{}, err
		}
		rankStart := time.Now()
		best := 0
		for i := range remaining {
			if remaining[i].score > remaining[best].score {
				best = i
			}
		}
		selected := &remaining[best]
		timing.Event("ranking_complete", rankStart, "selected", selected.candidate.Title, "score", selected.score, "needs_verification", selected.verify)
		if !selected.verify {
			return selected.candidate, nil
		}
		if verifications >= maxDiscoveryVerifications {
			remaining = append(remaining[:best], remaining[best+1:]...)
			continue
		}
		verifications++
		full, err := verify(selected.candidate)
		if err != nil {
			if ctx.Err() != nil {
				return Candidate{}, ctx.Err()
			}
			lastErr = err
			timing.Event("candidate_verification_failed", rankStart, "video_id", selected.candidate.ID, "error", err)
		}
		filterStart := time.Now()
		score, music := Score(query, full)
		timing.Event("music_filtering_complete", filterStart, "video_id", selected.candidate.ID, "accepted", err == nil && music)
		if err != nil || !music {
			remaining = append(remaining[:best], remaining[best+1:]...)
			continue
		}
		// Re-rank if the provisional official-channel bonus was not confirmed.
		selected.candidate, selected.score, selected.verify = full, score, false
	}
	if lastErr != nil {
		return Candidate{}, lastErr
	}
	return Candidate{}, ErrNoSong
}
