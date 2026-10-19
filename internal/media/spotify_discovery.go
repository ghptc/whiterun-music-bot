package media

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// Four discovery requests, at most eight unique full extractions, under the
// resolver's existing 90s deadline. No player/queue locks are acquired here.
func discoverSpotify(ctx context.Context, t Track, p MatchPolicy, query string, search func(string, string) ([]*Candidate, error), verify func(Candidate) (Candidate, error)) (chosen Candidate, err error) {
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
		for _, c := range entries {
			if c != nil && music {
				c.MusicSearch = true
			}
		}
		pools = append(pools, entries)
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
		if verified >= 8 {
			return Candidate{}, &MatchError{Status: MatchUncertain, Reason: "verification_budget_exhausted"}
		}
		verified++
		full, e := verify(c)
		if e == nil && full.ID != c.ID {
			e = errors.New("YouTube returned a different video ID")
		}
		cache[c.ID] = verification{full, e}
		return full, e
	}
	lookup("https://music.youtube.com/search?q="+url.QueryEscape(query)+"#songs", "spotify_music_search", true)
	// Resolve the official Music pool before paying for a second search. Only
	// independently corroborated releases may terminate this fast pass.
	if len(pools) > 0 {
		fast, fastErr := selectSpotifyPolicyMode(ctx, t, p, deduplicateCandidates(pools...), check, true)
		if fastErr == nil {
			return fast, nil
		}
	}
	lookup("ytsearch10:"+query, "spotify_video_search", false)
	for round := 0; round < 3; round++ {
		if ctx.Err() != nil {
			return Candidate{}, ctx.Err()
		}
		if len(pools) == 0 {
			return Candidate{}, errors.Join(failures...)
		}
		chosen, err = selectSpotifyPolicy(ctx, t, p, deduplicateCandidates(pools...), check)
		if err == nil {
			return chosen, nil
		}
		var decision *MatchError
		if !errors.As(err, &decision) {
			return Candidate{}, err
		}
		if round == 2 {
			break
		}
		alternative := t.Artist + " - " + t.Title + " official audio"
		if round == 1 {
			alternative = "\"" + t.Title + "\" \"" + t.Artist + "\" \"Topic\""
		}
		lookup("ytsearch10:"+alternative, "spotify_alternative_search", false)
	}
	// Failed discovery cannot prove absence or resolve an existing ambiguity.
	if len(failures) > 0 {
		return Candidate{}, errors.Join(failures...)
	}
	return Candidate{}, err
}
