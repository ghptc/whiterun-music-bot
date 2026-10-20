//go:build qa009_upstream_probe

package discord

import (
	"bytes"
	"context"
	"github.com/disgoorg/disgo/rest"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestUpstreamGlobalCooldownRace(t *testing.T) {
	limiter := rest.NewRateLimiter()
	a, b := limiterEndpoint("/qa/a"), limiterEndpoint("/qa/b")
	_ = limiter.Wait(context.Background(), a)
	_ = limiter.Wait(context.Background(), b)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for _, ep := range []*rest.CompiledEndpoint{a, b} {
		wg.Go(func() {
			<-gate
			_ = limiter.Unlock(ep, &http.Response{StatusCode: 429, Header: http.Header{
				"X-Ratelimit-Bucket": []string{"global"}, "X-Ratelimit-Global": []string{"true"}, "Retry-After": []string{"2"},
			}})
		})
	}
	close(gate)
	wg.Wait()
	// The process intentionally exits with the upstream worker still running;
	// the separate QA008 reproduction already demonstrates its Close leak.
}

// Observe multiple documented maintenance periods after graceful Close in an
// otherwise idle client. Run this probe alone, so unrelated goroutines cannot
// be mistaken for the limiter owned by this test.
func TestUpstreamLimiterPersistsAcrossCleanupPeriods(t *testing.T) {
	const interval = 10 * time.Millisecond
	cleaned := make(chan struct{})
	log := slog.New(&probeCleanupHandler{cleaned: cleaned})
	limiter := rest.NewRateLimiter(rest.WithCleanupInterval(interval), rest.WithRateLimiterLogger(log))
	endpoint := limiterEndpoint("/qa/cleanup")
	if err := limiter.Wait(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Unlock(endpoint, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("limiter did not complete its first cleanup")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	limiter.Close(closeCtx)
	// Observe beyond the caller's shutdown deadline, then three full configured
	// maintenance periods. This is real time in an isolated test process.
	<-closeCtx.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for cycle := 1; cycle <= 3; cycle++ {
		<-ticker.C
		stack := make([]byte, 128<<10)
		n := runtime.Stack(stack, true)
		if !bytes.Contains(stack[:n], []byte("rest.(*rateLimiterImpl).cleanup")) {
			return
		}
		t.Logf("worker persists after Close deadline, cycle=%d; blocked in doCleanup=%v", cycle, bytes.Contains(stack[:n], []byte("rest.(*rateLimiterImpl).doCleanup")))
		if cycle == 3 {
			t.Logf("%s", stack[:n])
		}
	}
	t.Fatal("QA008-D03: limiter worker persists after Close deadline and three cleanup periods")
}

type probeCleanupHandler struct {
	cleaned chan struct{}
	once    sync.Once
}

func (*probeCleanupHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *probeCleanupHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "cleaned up rate limit buckets" {
		h.once.Do(func() { close(h.cleaned) })
	}
	return nil
}
func (h *probeCleanupHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *probeCleanupHandler) WithGroup(string) slog.Handler      { return h }
