// Adapted from disgo v0.19.6 rest/rest_rate_limiter.go (Apache-2.0).
// Copyright disgo contributors. See licenses/disgo-Apache-2.0.txt.
// Keep the SDK bucket/header policy; add owned shutdown and synchronized
// global cooldowns. Remove this adapter once upstream provides these guarantees.
package discord

import (
	"context"
	"fmt"
	"github.com/disgoorg/disgo/rest"
	"github.com/sasha-s/go-csync"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// newManagedRESTLimiter preserves the pinned SDK policy with an owned lifecycle.
func newManagedRESTLimiter(log *slog.Logger) *managedRESTLimiter {
	ctx, cancel := context.WithCancel(context.Background())

	rateLimiter := &managedRESTLimiter{logger: log, buckets: map[string]*restBucket{}, ctx: ctx, cancel: cancel, done: make(chan struct{})}

	go rateLimiter.cleanup()

	return rateLimiter
}

type managedRESTLimiter struct {
	logger    *slog.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closed    bool
	globalMu  sync.Mutex

	// global Rate Limit
	global time.Time

	// Hash + Major Parameter -> bucket
	buckets   map[string]*restBucket
	bucketsMu sync.Mutex
}

func (l *managedRESTLimiter) MaxRetries() int {
	return rest.MaxRetries
}

func (l *managedRESTLimiter) cleanup() {
	defer close(l.done)
	ticker := time.NewTicker(rest.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.doCleanup()
		}
	}
}
func (l *managedRESTLimiter) doCleanup() {
	l.bucketsMu.Lock()
	defer l.bucketsMu.Unlock()
	before := len(l.buckets)
	now := time.Now()
	for hash, b := range l.buckets {
		if !b.mu.TryLock() {
			continue
		}
		if b.Reset.Before(now) {
			l.logger.Debug("cleaning up bucket", slog.String("hash", hash), slog.String("id", b.ID), slog.Time("reset", b.Reset))
			delete(l.buckets, hash)
		}
		b.mu.Unlock()
	}
	if before != len(l.buckets) {
		l.logger.Debug("cleaned up rate limit buckets", slog.Int("before", before), slog.Int("after", len(l.buckets)), slog.Int("removed", before-len(l.buckets)))
	}
}

func (l *managedRESTLimiter) Close(ctx context.Context) {
	l.closeOnce.Do(func() {
		l.bucketsMu.Lock()
		l.closed = true
		l.bucketsMu.Unlock()
		l.cancel()
	})
	<-l.done
	// Snapshot without holding the map lock while draining requests: Unlock
	// needs that same lock to find its bucket.
	l.bucketsMu.Lock()
	buckets := make([]*restBucket, 0, len(l.buckets))
	for _, bucket := range l.buckets {
		buckets = append(buckets, bucket)
	}
	l.bucketsMu.Unlock()
	var workers sync.WaitGroup
	for _, bucket := range buckets {
		workers.Go(func() {
			if err := bucket.mu.CLock(ctx); err == nil {
				bucket.mu.Unlock()
			}
		})
	}
	workers.Wait()
}
func (l *managedRESTLimiter) Reset() {
	l.bucketsMu.Lock()
	defer l.bucketsMu.Unlock()

	l.globalMu.Lock()
	l.global = time.Time{}
	l.globalMu.Unlock()
	clear(l.buckets)
}

func (l *managedRESTLimiter) getRouteHash(endpoint *rest.CompiledEndpoint) string {
	hash := endpoint.Endpoint.Method + "+" + endpoint.Endpoint.Route

	if endpoint.MajorParams != "" {
		hash += "+" + endpoint.MajorParams
	}
	return hash
}

func (l *managedRESTLimiter) getBucket(endpoint *rest.CompiledEndpoint, create bool) *restBucket {
	hash := l.getRouteHash(endpoint)

	l.logger.Debug("locking buckets")
	l.bucketsMu.Lock()
	defer func() {
		l.logger.Debug("unlocking buckets")
		l.bucketsMu.Unlock()
	}()
	if create && l.closed {
		return nil
	}
	b, ok := l.buckets[hash]
	if !ok {
		if !create {
			return nil
		}

		b = &restBucket{
			Remaining: 1,
			// we don't know the limit yet
			Limit: -1,
		}
		l.buckets[hash] = b
	}
	return b
}

func (l *managedRESTLimiter) Wait(ctx context.Context, endpoint *rest.CompiledEndpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	waitCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	defer func() { stop(); cancel() }()
	ctx = waitCtx
	b := l.getBucket(endpoint, true)
	if b == nil || l.ctx.Err() != nil {
		return context.Canceled
	}
	if err := b.mu.CLock(ctx); err != nil {
		return err
	}
	l.logger.Debug("locking rest bucket", slog.String("id", b.ID), slog.Int("limit", b.Limit), slog.Int("remaining", b.Remaining), slog.Time("reset", b.Reset))

	var until time.Time
	now := time.Now()

	if b.Remaining == 0 && b.Reset.After(now) {
		until = b.Reset
	} else {
		l.globalMu.Lock()
		until = l.global
		l.globalMu.Unlock()
	}

	if until.After(now) {
		// TODO: do we want to return early when we know the rate limit bigger than ctx deadline?
		if deadline, ok := ctx.Deadline(); ok && until.After(deadline) {
			b.mu.Unlock()
			return context.DeadlineExceeded
		}

		select {
		case <-ctx.Done():
			b.mu.Unlock()
			return ctx.Err()
		case <-time.After(until.Sub(now)):
		}
	}
	return nil
}

func (l *managedRESTLimiter) Unlock(endpoint *rest.CompiledEndpoint, rs *http.Response) error {
	b := l.getBucket(endpoint, false)
	if b == nil {
		return nil
	}
	defer func() {
		l.logger.Debug("unlocking rest bucket", slog.String("id", b.ID), slog.Int("limit", b.Limit), slog.Int("remaining", b.Remaining), slog.Time("reset", b.Reset))
		b.mu.Unlock()
	}()

	// no response provided means we can't update anything and just unlock it
	if rs == nil || rs.Header == nil {
		return nil
	}
	bucketHeader := rs.Header.Get("X-RateLimit-Bucket")

	// if we don't have a bucket header, we can't update anything
	if bucketHeader == "" {
		return nil
	}

	b.ID = bucketHeader

	global := rs.Header.Get("X-RateLimit-Global") != ""
	cloudflare := rs.Header.Get("via") == ""
	remainingHeader := rs.Header.Get("X-RateLimit-Remaining")
	limitHeader := rs.Header.Get("X-RateLimit-Limit")
	resetHeader := rs.Header.Get("X-RateLimit-Reset")
	resetAfterHeader := rs.Header.Get("X-RateLimit-Reset-After")
	retryAfterHeader := rs.Header.Get("Retry-After")

	l.logger.Debug("ratelimit response headers", slog.Int("code", rs.StatusCode), slog.Bool("global", global), slog.Bool("cloudflare", cloudflare), slog.String("remaining", remainingHeader), slog.String("limit", limitHeader), slog.String("reset", resetHeader), slog.String("reset_after", resetAfterHeader), slog.String("retry_after", retryAfterHeader))

	// we hit a rate limit. let's see if it was global cloudflare or a route specific one
	if rs.StatusCode == http.StatusTooManyRequests {
		retryAfter, err := strconv.Atoi(retryAfterHeader)
		if err != nil {
			return fmt.Errorf("invalid retryAfter %s: %w", retryAfterHeader, err)
		}
		reset := time.Now().Add(time.Second * time.Duration(retryAfter))
		if global {
			l.globalMu.Lock()
			l.global = reset
			l.globalMu.Unlock()
			l.logger.Warn("global rate limit exceeded", slog.Int("retry_after", retryAfter))
		} else if cloudflare {
			l.globalMu.Lock()
			l.global = reset
			l.globalMu.Unlock()
			l.logger.Warn("cloudflare rate limit exceeded", slog.Int("retry_after", retryAfter))
		} else {
			b.Remaining = 0
			b.Reset = reset
			l.logger.Warn("rate limit exceeded", slog.String("endpoint", endpoint.URL), slog.Int("retry_after", retryAfter))
		}
		return nil
	}

	if limitHeader != "" {
		limit, err := strconv.Atoi(limitHeader)
		if err != nil {
			return fmt.Errorf("invalid limit %s: %w", limitHeader, err)
		}
		b.Limit = limit
	}

	if remainingHeader != "" {
		remaining, err := strconv.Atoi(remainingHeader)
		if err != nil {
			return fmt.Errorf("invalid remaining %s: %w", remainingHeader, err)
		}
		b.Remaining = remaining
	}

	// we prioritize the reset after header over the reset header as it's more accurate due to clock differences
	if resetAfterHeader != "" {
		resetAfter, err := strconv.ParseFloat(resetAfterHeader, 64)
		if err != nil {
			return fmt.Errorf("invalid reset after %s: %w", resetAfterHeader, err)
		}

		b.Reset = time.Now().Add(time.Duration(resetAfter) * time.Second)
	} else if resetHeader != "" {
		reset, err := strconv.ParseFloat(resetHeader, 64)
		if err != nil {
			return fmt.Errorf("invalid reset %s: %w", resetHeader, err)
		}

		sec := int64(reset)
		b.Reset = time.Unix(sec, int64((reset-float64(sec))*float64(time.Second)))
	} else {
		return fmt.Errorf("no reset or reset after header found in response")
	}
	return nil
}

type restBucket struct {
	mu        csync.Mutex
	ID        string
	Reset     time.Time
	Remaining int
	Limit     int
}
