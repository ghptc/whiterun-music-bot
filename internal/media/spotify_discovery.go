package media

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// Four bounded discovery requests and sequential unique full extractions under the
// resolver's existing 90s deadline. No player/queue locks are acquired here.
func discoverSpotify(ctx context.Context, t Track, p MatchPolicy, query string, search func(string, string) ([]*Candidate, error), verify func(Candidate) (Candidate, error)) (chosen Candidate, err error) {
	if err := p.Validate(); err != nil {
		return Candidate{}, err
	}
	started := time.Now()
	attempts, verified := 0, 0
	timing := TimingFrom(ctx)
	defer func() {
		reason := "selected"
		var m *MatchError
		if errors.As(err, &m) {
			reason = m.Reason
		} else if err != nil {
			reason = "infrastructure_or_context"
		}
		timing.Event("spotify_resolution", started, "spotify_track_id", t.SpotifyID, "search_attempts", attempts, "verification_attempts", verified, "reason", reason, "selected_video_id", chosen.ID)
	}()
	var pools [][]*Candidate
	var failures []error
	lookup := func(target, purpose string, music bool) {
		if ctx.Err() != nil {
			return
		}
		attempts++
		entries, e := search(target, purpose)
		if e != nil {
			failures = append(failures, e)
			return
		}
		pool := make([]*Candidate, 0, len(entries))
		for _, c := range entries {
			if c != nil {
				copy := *c
				copy.MusicSearch = copy.MusicSearch || music
				pool = append(pool, &copy)
			}
		}
		pools = append(pools, pool)
	}
	type verification struct {
		c   Candidate
		err error
	}
	cache := map[string]verification{}
	check := func(c Candidate) (Candidate, error) {
		if v, ok := cache[c.ID]; ok {
			return v.c, v.err
		}
		verified++
		full, e := verify(c)
		if e == nil && full.ID != c.ID {
			e = errors.New("YouTube returned a different video ID")
		}
		cache[c.ID] = verification{full, e}
		return full, e
	}
	queries := []struct {
		target, purpose string
		music           bool
	}{
		{"https://music.youtube.com/search?q=" + url.QueryEscape(query) + "#songs", "spotify_music_search", true},
		{"ytsearch10:" + query, "spotify_video_search", false},
		{"ytsearch10:" + t.Artist + " - " + t.Title + " official audio", "spotify_alternative_search", false},
		{"ytsearch10:\"" + t.Title + "\" \"" + t.Artist + "\" \"Topic\"", "spotify_alternative_search", false},
	}
	var uncertain bool
	for _, q := range queries {
		if ctx.Err() != nil {
			return Candidate{}, ctx.Err()
		}
		lookup(q.target, q.purpose, q.music)
		if len(pools) == 0 {
			continue
		}
		chosen, err = selectSpotifyPolicy(ctx, t, p, deduplicateCandidates(pools...), check)
		if err == nil {
			return chosen, nil
		}
		if ctx.Err() != nil {
			return Candidate{}, ctx.Err()
		}
		var decision *MatchError
		if errors.As(err, &decision) {
			uncertain = uncertain || decision.Status == MatchUncertain
		} else {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return Candidate{}, errors.Join(failures...)
	}
	if uncertain {
		return Candidate{}, &MatchError{Status: MatchUncertain, Reason: "insufficient_metadata"}
	}
	return Candidate{}, &MatchError{Status: MatchRejected, Reason: "no_acceptable_candidate"}
}
