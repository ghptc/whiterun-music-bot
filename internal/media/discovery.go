package media

import (
	"context"
	"time"
)

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
	for _, c := range candidates {
		if !videoID.MatchString(c.ID) {
			continue
		}
		shape := c
		if shape.Duration == 0 {
			shape.Duration = 45
		} // Missing duration needs verification.
		if !musicShape(shape) || c.LiveStatus == "is_live" {
			continue
		}
		provisional := c
		uncertainArtist := c.Verified && c.Artist == "" && len(c.Categories) == 0 && channelMatchesQuery(query, c)
		if uncertainArtist {
			provisional.Artist = c.Channel
		}
		score, ok := scoreMusic(query, provisional)
		if !ok {
			continue
		}
		remaining = append(remaining, ranked{c, score, !IsMusic(c) || uncertainArtist})
	}
	timing.Event("discovery_ranking_complete", start, "candidates", len(candidates), "eligible", len(remaining))
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
