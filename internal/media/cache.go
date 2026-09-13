package media

import (
	"context"
	"strings"
	"sync"
	"time"
)

const searchCacheTTL = 10 * time.Minute
const searchCacheLimit = 256

type cacheEntry struct {
	track   Track
	expires time.Time
}

// SearchCache stores successful metadata only, never signed stream URLs.
// It is bounded and shared by resolver copies. Identical concurrent misses
// share one search; each caller retains independent cancellation.
type SearchCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	flights map[string]*searchFlight
}

func (c *SearchCache) get(query string, now time.Time) (Track, bool) {
	if c == nil {
		return Track{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[query]
	if !ok || !now.Before(entry.expires) {
		delete(c.entries, query)
		return Track{}, false
	}
	return entry.track, true
}

func (c *SearchCache) put(query string, track Track, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(query, track, now)
}

func (c *SearchCache) putLocked(query string, track Track, now time.Time) {
	if c.entries == nil {
		c.entries = make(map[string]cacheEntry)
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
	if _, exists := c.entries[query]; !exists && len(c.entries) >= searchCacheLimit {
		var oldest string
		var expiry time.Time
		for key, entry := range c.entries {
			if expiry.IsZero() || entry.expires.Before(expiry) {
				oldest, expiry = key, entry.expires
			}
		}
		delete(c.entries, oldest)
	}
	track.Timing = nil // A cache hit must belong to the new interaction.
	c.entries[query] = cacheEntry{track: track, expires: now.Add(searchCacheTTL)}
}

// Normalize text only. Canonical YouTube URLs are separate keys because video
// IDs are case-sensitive. Variant words and punctuation are retained.
func normalizeQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

type searchFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	track   Track
	err     error
}

func (c *SearchCache) resolve(ctx context.Context, key string, work func(context.Context) (Track, error)) (Track, error) {
	if c == nil {
		return work(ctx)
	}
	if err := ctx.Err(); err != nil {
		return Track{}, err
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && time.Now().Before(entry.expires) {
		c.mu.Unlock()
		return entry.track, nil
	}
	if c.flights == nil {
		c.flights = make(map[string]*searchFlight)
	}
	flight, shared := c.flights[key]
	if !shared {
		// The search belongs to all waiters, not the first caller's guild. The last
		// departing waiter cancels it; resolveUncached also imposes a 90s deadline.
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		flight = &searchFlight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = flight
		go func() {
			track, err := work(workCtx)
			track.Timing = nil
			c.mu.Lock()
			flight.track, flight.err = track, err
			if c.flights[key] == flight {
				if err == nil && workCtx.Err() == nil {
					c.putLocked(key, track, time.Now())
				}
				delete(c.flights, key)
			}
			close(flight.done)
			c.mu.Unlock()
			cancel()
		}()
	}
	flight.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			if c.flights[key] == flight {
				delete(c.flights, key)
			}
			flight.cancel()
		}
		c.mu.Unlock()
	}()
	started := time.Now()
	if shared {
		TimingFrom(ctx).Event("resolver_coalesced", started)
	}
	select {
	case <-ctx.Done():
		return Track{}, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return Track{}, err
		}
		return flight.track, flight.err
	}
}
