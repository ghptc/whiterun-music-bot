package player

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestAdvancedStartupDeadlines(t *testing.T) {
	for _, scenario := range []string{"resolution deadline", "first frame deadline", "frame before resolution", "frame before deadline", "parent cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				resolved, audio := make(chan struct{}), make(chan struct{})
				done := make(chan struct{})
				go func() { defer close(done); watchStartup(ctx, cancel, resolved, audio, 10*time.Second, 5*time.Second) }()
				synctest.Wait()
				switch scenario {
				case "resolution deadline":
					time.Sleep(10*time.Second - time.Nanosecond)
					if ctx.Err() != nil {
						t.Fatal("early resolution timeout")
					}
					time.Sleep(time.Nanosecond)
					synctest.Wait()
					if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) || !strings.Contains(context.Cause(ctx).Error(), "resolution") {
						t.Fatal(context.Cause(ctx))
					}
				case "first frame deadline":
					close(resolved)
					synctest.Wait()
					time.Sleep(5 * time.Second)
					synctest.Wait()
					if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) || !strings.Contains(context.Cause(ctx).Error(), "first Opus") {
						t.Fatal(context.Cause(ctx))
					}
				case "frame before resolution":
					close(audio)
					synctest.Wait()
					time.Sleep(time.Hour)
					if ctx.Err() != nil {
						t.Fatal("resolution timer owns completed startup")
					}
				case "frame before deadline":
					close(resolved)
					synctest.Wait()
					time.Sleep(5*time.Second - time.Nanosecond)
					close(audio)
					synctest.Wait()
					time.Sleep(time.Hour)
					if ctx.Err() != nil {
						t.Fatal("timer canceled active audio")
					}
				case "parent cancellation":
					cancel(context.Canceled)
					synctest.Wait()
				}
				select {
				case <-done:
				default:
					t.Fatal("monitor goroutine leaked")
				}
			})
		})
	}
}
