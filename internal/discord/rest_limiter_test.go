package discord

import (
	"context"
	"errors"
	"github.com/disgoorg/disgo/rest"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// QA008-D03: SDK client Close must join the application-owned maintenance loop.
func TestDefectRESTClientCloseJoinsLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := newManagedRESTLimiter(slog.New(slog.NewTextHandler(io.Discard, nil)))
		client := rest.NewClient("fixture", rest.WithRateLimiter(limiter))
		client.Close(context.Background())
		time.Sleep(3 * rest.CleanupInterval)
		synctest.Wait()
		client.Close(context.Background())
		select {
		case <-limiter.done:
		default:
			t.Fatal("maintenance worker survived")
		}
	})
}

func limiterForTest() *managedRESTLimiter {
	return newManagedRESTLimiter(slog.New(slog.NewTextHandler(io.Discard, nil)))
}
func limiterEndpoint(path string) *rest.CompiledEndpoint {
	return (&rest.Endpoint{Method: "GET", Route: path}).Compile(nil)
}
func TestDefectLimiterCloseDrainsAndCancelsWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := limiterForTest()
		endpoint := limiterEndpoint("/qa/active")
		if err := limiter.Wait(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		waiting := make(chan error, 1)
		go func() { waiting <- limiter.Wait(context.Background(), endpoint) }()
		synctest.Wait()
		closed := make(chan struct{})
		go func() { limiter.Close(context.Background()); close(closed) }()
		synctest.Wait()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatal("pending request survived Close", err)
		}
		select {
		case <-closed:
			t.Fatal("Close did not wait for active request")
		default:
		}
		if err := limiter.Unlock(endpoint, nil); err != nil {
			t.Fatal(err)
		}
		<-closed
		if err := limiter.Wait(context.Background(), limiterEndpoint("/qa/new")); !errors.Is(err, context.Canceled) {
			t.Fatal("closed client accepted work", err)
		}
		limiter.Close(context.Background())
	})
}
func TestDefectLimiterCloseDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := limiterForTest()
		endpoint := limiterEndpoint("/qa/busy")
		if err := limiter.Wait(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		limiter.Close(ctx)
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatal("active request was not bounded by Close deadline")
		}
		if err := limiter.Unlock(endpoint, nil); err != nil {
			t.Fatal(err)
		}
		limiter.Close(context.Background())
	})
}
func TestDefectLimiterGlobalCooldownConcurrentBuckets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := limiterForTest()
		defer limiter.Close(context.Background())
		first, second := limiterEndpoint("/qa/one"), limiterEndpoint("/qa/two")
		if err := limiter.Wait(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		if err := limiter.Wait(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		response := func() *http.Response {
			return &http.Response{StatusCode: 429, Header: http.Header{
				"X-Ratelimit-Bucket": []string{"global"}, "X-Ratelimit-Global": []string{"true"}, "Retry-After": []string{"2"},
			}}
		}
		var wg sync.WaitGroup
		for _, endpoint := range []*rest.CompiledEndpoint{first, second} {
			wg.Go(func() {
				if err := limiter.Unlock(endpoint, response()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := limiter.Wait(ctx, limiterEndpoint("/qa/third")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("global cooldown bypassed", err)
		}
		limiter.Reset()
		endpoint := limiterEndpoint("/qa/after-reset")
		if err := limiter.Wait(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		if err := limiter.Unlock(endpoint, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDefectLimiterConcurrentSameBucket(t *testing.T) {
	limiter := limiterForTest()
	defer limiter.Close(context.Background())
	endpoint := limiterEndpoint("/qa/same")
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			for range 50 {
				if err := limiter.Wait(context.Background(), endpoint); err != nil {
					t.Error(err)
					return
				}
				response := &http.Response{StatusCode: 200, Header: http.Header{
					"X-Ratelimit-Bucket":      []string{"same"},
					"X-Ratelimit-Limit":       []string{"1"},
					"X-Ratelimit-Remaining":   []string{"1"},
					"X-Ratelimit-Reset-After": []string{"0"},
				}}
				if err := limiter.Unlock(endpoint, response); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
}

// Header parsing failures must release the route lock, allowing later requests.
func TestDefectLimiterMalformedHeadersReleaseBucket(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		status           int
	}{
		{"retry", "Retry-After", "invalid", 429},
		{"limit", "X-Ratelimit-Limit", "invalid", 200},
		{"remaining", "X-Ratelimit-Remaining", "invalid", 200},
		{"reset after", "X-Ratelimit-Reset-After", "invalid", 200},
		{"reset", "X-Ratelimit-Reset", "invalid", 200},
		{"missing reset", "X-Ratelimit-Limit", "1", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := limiterForTest()
				defer limiter.Close(context.Background())
				endpoint := limiterEndpoint("/qa/headers")
				if err := limiter.Wait(context.Background(), endpoint); err != nil {
					t.Fatal(err)
				}
				headers := http.Header{"X-Ratelimit-Bucket": []string{"fixture"}}
				headers.Set(tc.key, tc.value)
				if err := limiter.Unlock(endpoint, &http.Response{StatusCode: tc.status, Header: headers}); err == nil {
					t.Fatal("malformed headers accepted")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := limiter.Wait(ctx, endpoint); err != nil {
					t.Fatal("route lock retained after parse failure", err)
				}
				if err := limiter.Unlock(endpoint, nil); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestDefectLimiterRouteCooldownIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := limiterForTest()
		defer limiter.Close(context.Background())
		first, other := limiterEndpoint("/qa/limited"), limiterEndpoint("/qa/free")
		if err := limiter.Wait(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		headers := http.Header{"X-Ratelimit-Bucket": []string{"fixture"}, "Retry-After": []string{"2"}, "Via": []string{"discord"}}
		if err := limiter.Unlock(first, &http.Response{StatusCode: 429, Header: headers}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := limiter.Wait(ctx, first); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("route limit bypassed", err)
		}
		if err := limiter.Wait(ctx, other); err != nil {
			t.Fatal("unrelated route blocked", err)
		}
		if err := limiter.Unlock(other, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if err := limiter.Wait(context.Background(), first); err != nil {
			t.Fatal("expired cooldown retained", err)
		}
		if err := limiter.Unlock(first, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDefectBotConstructionFailureClosesLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, err := New(context.Background(), "invalid-token", 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			t.Fatal("invalid token accepted")
		}
		synctest.Wait()
		// A live maintenance worker makes synctest reject the bubble on return.
	})
}
