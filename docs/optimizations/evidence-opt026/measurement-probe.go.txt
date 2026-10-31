package media

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// Identical offline probe runs on the baseline and optimized trees. Counts are
// extractor processes, never inferred HTTP requests. No network is contacted.
func TestOPT026Measurement(t *testing.T) {
	ctx := context.Background()
	r, calls := accessProbe(t, false)
	var cold, warm []float64
	for i := 1; i <= 25; i++ {
		start := time.Now()
		tr, err := r.MatchSpotify(ctx, accessTrack(i), true)
		if err != nil {
			t.Fatal(err)
		}
		cold = append(cold, float64(time.Since(start).Microseconds())/1000)
		start = time.Now()
		if _, err = r.MatchSpotify(ctx, accessTrack(i), true); err != nil {
			t.Fatal(err)
		}
		warm = append(warm, float64(time.Since(start).Microseconds())/1000)
		if tr.VideoID == "" {
			t.Fatal("missing match")
		}
	}
	percentile := func(samples []float64, p float64) float64 {
		sort.Float64s(samples)
		return samples[int(float64(len(samples)-1)*p)]
	}
	s, v := accessCounts(t, calls)
	accessMetric(t, "cold_warm", map[string]any{"tracks": 25, "search": s, "verification": v, "cold_p50_ms": percentile(cold, .50), "cold_p95_ms": percentile(cold, .95), "warm_p50_ms": percentile(warm, .50), "warm_p95_ms": percentile(warm, .95)})
	tr, err := r.MatchSpotify(ctx, accessTrack(1), true)
	if err != nil {
		t.Fatal(err)
	}
	expire := func() {
		r.YouTube.Cache.mu.Lock()
		r.YouTube.Cache.entries = nil
		r.YouTube.Cache.mu.Unlock()
		tr.identityProof.CheckedAt = time.Now().Add(-11 * time.Minute)
		store := r.YouTube.Mappings.(*accessProbeMappings)
		store.mu.Lock()
		for k, c := range store.items {
			c.CheckedAt = tr.identityProof.CheckedAt
			store.items[k] = c
		}
		store.mu.Unlock()
	}
	expire()
	_, before := accessCounts(t, calls)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := r.YouTube.VerifySpotify(ctx, tr); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	_, after := accessCounts(t, calls)
	accessMetric(t, "five_concurrent", map[string]any{"verification": after - before})
	expire()
	_, before = accessCounts(t, calls)
	if err := r.YouTube.VerifySpotify(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.YouTube.VerifySpotify(ctx, tr); err != nil {
		t.Fatal(err)
	}
	_, after = accessCounts(t, calls)
	accessMetric(t, "expired_sequential", map[string]any{"verification": after - before})
	r, calls = accessProbe(t, false)
	for i := 1; i <= 123; i++ {
		if _, err := r.MatchSpotify(ctx, accessTrack(i), true); err != nil {
			t.Fatal(err)
		}
	}
	s, v = accessCounts(t, calls)
	accessMetric(t, "123_cold_positions", map[string]any{"search": s, "verification": v, "positions": 123})
}
