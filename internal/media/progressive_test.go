package media

import (
	"context"
	"testing"
	"time"
)

func TestMatchLimiterReservesFirstCapacityAndReleasesCanceledWaiters(t *testing.T) {
	limiter := NewMatchLimiter()
	var releases []func()
	for range 3 {
		release, err := limiter.acquire(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := limiter.acquire(ctx, true)
	if err != nil {
		t.Fatal("background consumed first-track capacity", err)
	}
	for _, priority := range []bool{false, true} {
		waiting, stop := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			release, err := limiter.acquire(waiting, priority)
			if release != nil {
				release()
			}
			done <- err
		}()
		stop()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("limiter waiter leaked")
		}
	}
	first()
	for _, release := range releases {
		release()
	}
	if len(limiter.all) != 0 || len(limiter.background) != 0 {
		t.Fatal("limiter slots leaked")
	}
	release, err := limiter.acquire(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
