package media

import (
	"context"
	"testing"
	"time"
)

func TestOPT026123WarmAndReopen(t *testing.T) {
	r, calls := accessProbe(t, false)
	ctx := context.Background()
	tracks := make([]Track, 123)
	for i := range tracks {
		var err error
		tracks[i], err = r.MatchSpotify(ctx, accessTrack(i+1), false)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.YouTube.VerifySpotify(ctx, tracks[i]); err != nil {
			t.Fatal(err)
		}
	}
	s, v := accessCounts(t, calls)
	if s != 123 || v != 123 {
		t.Fatal(s, v)
	}
	accessMetric(t, "123_distinct_cold", map[string]any{"search_processes": s, "verification_processes": v, "metadata_processes": s + v, "playback_identity_extra_processes": 0, "media_processes_not_executed": 123})
	for i := range tracks {
		if _, err := r.MatchSpotify(ctx, accessTrack(i+1), false); err != nil {
			t.Fatal(err)
		}
	}
	s2, v2 := accessCounts(t, calls)
	if s2 != s || v2 != v {
		t.Fatal("warm misses", s2, v2)
	}
	accessMetric(t, "123_ram_warm", map[string]any{"additional_metadata_processes": s2 + v2 - s - v})
	r.YouTube.Cache = &SearchCache{}
	for i := range tracks {
		if _, err := r.MatchSpotify(ctx, accessTrack(i+1), false); err != nil {
			t.Fatal(err)
		}
	}
	s2, v2 = accessCounts(t, calls)
	if s2 != s || v2 != v {
		t.Fatal("fresh mappings missed", s2, v2)
	}
	accessMetric(t, "123_mapping_fresh_ram_empty", map[string]any{"additional_metadata_processes": s2 + v2 - s - v})
	store := r.YouTube.Mappings.(*accessProbeMappings)
	store.mu.Lock()
	for k, c := range store.items {
		c.CheckedAt = time.Now().Add(-11 * time.Minute)
		store.items[k] = c
	}
	store.mu.Unlock()
	r.YouTube.Cache = &SearchCache{}
	for i := range tracks {
		if _, err := r.MatchSpotify(ctx, accessTrack(i+1), false); err != nil {
			t.Fatal(err)
		}
	}
	s2, v2 = accessCounts(t, calls)
	if s2 != 123 || v2 != 246 {
		t.Fatal(s2, v2)
	}
	accessMetric(t, "123_mapping_stale_ram_empty", map[string]any{"additional_searches": s2 - s, "additional_verifications": v2 - v})
}
