package media

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchCacheExpiryBoundAndRequestIsolation(t *testing.T) {
	c := &SearchCache{}
	now := time.Now()
	original := Track{Title: "song", Timing: &Timing{}}
	c.put("song", original, now)
	hit, ok := c.get("song", now.Add(searchCacheTTL-time.Nanosecond))
	if !ok || hit.Title != "song" || hit.Timing != nil {
		t.Fatalf("bad cached track: %#v", hit)
	}
	if _, ok := c.get("song", now.Add(searchCacheTTL)); ok {
		t.Fatal("expired entry returned")
	}
	for i := range searchCacheLimit + 1 {
		c.put(time.Duration(i).String(), original, now.Add(time.Duration(i)))
	}
	if len(c.entries) != searchCacheLimit {
		t.Fatalf("cache size %d", len(c.entries))
	}
	if _, ok := c.get("0s", now); ok {
		t.Fatal("oldest entry not evicted")
	}
}

func TestResolverCacheReusesMetadataButKeepsVariantsSeparate(t *testing.T) {
	r, _ := resolverFixture(t, `{"entries":[{"id":"abcdefghijk","title":"Arctic Monkeys 505 Official Audio","duration":240,"categories":["Music"]}]}`)
	r.Cache = &SearchCache{}
	ctx := context.Background()
	first, err := r.Resolve(ctx, "Arctic Monkeys 505")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.Binary); err != nil {
		t.Fatal(err)
	}
	hit, err := r.Resolve(ctx, " Arctic Monkeys 505 ")
	if err != nil || hit != first {
		t.Fatalf("cache miss: %#v %v", hit, err)
	}
	if _, err := r.Resolve(ctx, "Arctic Monkeys 505 live"); err == nil {
		t.Fatal("variant shared cache key")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Resolve(cancelled, "Arctic Monkeys 505"); err == nil {
		t.Fatal("cancelled search returned cached track")
	}
}

func TestResolverDoesNotCacheFailure(t *testing.T) {
	r, _ := resolverFixture(t, `{"entries":[]}`)
	r.Cache = &SearchCache{}
	if _, err := r.Resolve(context.Background(), "song"); err == nil {
		t.Fatal("expected rejection")
	}
	if len(r.Cache.entries) != 0 {
		t.Fatal("cached failure")
	}
}

func TestSearchCacheConcurrentAccess(t *testing.T) {
	c := &SearchCache{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			key := time.Duration(i).String()
			for range 100 {
				c.put(key, Track{Title: key}, time.Now())
				if track, ok := c.get(key, time.Now()); !ok || track.Title != key {
					t.Errorf("missing track for %s", key)
				}
			}
		})
	}
	wg.Wait()
}

func TestNormalizedQueryKeepsVariantsAndURLCase(t *testing.T) {
	if normalizeQuery("  Arctic  MONKEYS\t505 ") != "arctic monkeys 505" {
		t.Fatal("normalization")
	}
	if normalizeQuery("505") == normalizeQuery("505 live") {
		t.Fatal("variant collision")
	}
	r, _ := resolverFixture(t, `{"entries":[{"id":"abcdefghijk","title":"Arctic Monkeys 505 Official Audio","duration":240}]}`)
	r.Cache = &SearchCache{}
	first, err := r.Resolve(context.Background(), "Arctic Monkeys 505")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.Binary); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(context.Background(), "arctic   MONKEYS 505")
	if err != nil || got != first {
		t.Fatalf("not normalized: %+v %v", got, err)
	}
	// YouTube IDs are case sensitive; URL keys must not use text normalization.
	r.Cache.put("https://www.youtube.com/watch?v=Abcdefghijk", first, time.Now())
	if _, err := r.Resolve(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk"); err == nil {
		t.Fatal("URL IDs were lowercased")
	}
}

func waitForWaiters(t *testing.T, cache *SearchCache, key string, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		cache.mu.Lock()
		f := cache.flights[key]
		ready := f != nil && f.waiters == n
		cache.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatal("waiters did not join")
		default:
			runtime.Gosched()
		}
	}
}

func TestCoalescedSearchHasIndependentCancellation(t *testing.T) {
	cache := &SearchCache{}
	var calls atomic.Int32
	release := make(chan struct{})
	work := func(ctx context.Context) (Track, error) {
		calls.Add(1)
		select {
		case <-release:
			return Track{Title: "song"}, nil
		case <-ctx.Done():
			return Track{}, ctx.Err()
		}
	}
	leader, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := cache.resolve(leader, "song", work); first <- err }()
	waitForWaiters(t, cache, "song", 1)
	go func() {
		track, err := cache.resolve(context.Background(), "song", work)
		if err == nil && track.Title != "song" {
			err = errors.New("wrong track")
		}
		second <- err
	}()
	waitForWaiters(t, cache, "song", 2)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("follower: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("searches=%d", calls.Load())
	}
	if _, ok := cache.get("song", time.Now()); !ok {
		t.Fatal("success not cached")
	}
}

func TestAllWaitersCancelSearchAndFailuresAreNotCached(t *testing.T) {
	cache := &SearchCache{}
	stopped := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := cache.resolve(ctx, "song", func(ctx context.Context) (Track, error) { <-ctx.Done(); close(stopped); return Track{}, ctx.Err() })
		finished <- err
	}()
	waitForWaiters(t, cache, "song", 1)
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("subprocess work not cancelled")
	}
	calls := 0
	for range 2 {
		_, err := cache.resolve(context.Background(), "song", func(context.Context) (Track, error) { calls++; return Track{}, errors.New("failed") })
		if err == nil {
			t.Fatal("lost error")
		}
	}
	if calls != 2 {
		t.Fatal("failure cached")
	}
}
