package player

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
	"whiterun/internal/media"
)

// Keep the cancelled stream alive until explicitly released. Observing only
// eventual completion would miss a successor overlapping subprocess teardown.
func TestQualityTeardownSerializesSuccessor(t *testing.T) {
	for _, operation := range []string{"skip", "replay", "jump", "stop", "leave"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan string, 8)
			canceled := make(chan struct{})
			teardown := make(chan struct{})
			var active atomic.Int32
			var overlap atomic.Bool
			p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error {
				if active.Add(1) != 1 {
					overlap.Store(true)
				}
				defer active.Add(-1)
				started <- tr.Title
				<-ctx.Done()
				if tr.Title == "first" {
					select {
					case <-canceled:
					default:
						close(canceled)
					}
					<-teardown
				}
				return ctx.Err()
			})
			released := false
			defer func() {
				if !released {
					close(teardown)
				}
			}()
			add(t, p, "first")
			receive(t, started, "first")
			add(t, p, "next")
			done := make(chan struct{})
			go func() {
				defer close(done)
				switch operation {
				case "skip":
					p.Skip()
				case "replay":
					_ = p.Replay()
				case "jump":
					_ = p.Jump(1)
				case "stop":
					p.Stop()
				case "leave":
					p.Leave(context.Background())
				}
			}()
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("control failed to cancel stream")
			}
			// The barrier guarantees the original stream has not finished.
			if active.Load() != 1 {
				t.Fatal("stream escaped teardown barrier")
			}
			select {
			case title := <-started:
				t.Fatal("started before teardown", title)
			default:
			}
			close(teardown)
			released = true
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("control did not complete")
			}
			switch operation {
			case "skip", "jump":
				receive(t, started, "next")
			case "replay":
				receive(t, started, "first")
			}
			p.Close(context.Background())
			if active.Load() != 0 || overlap.Load() {
				t.Fatal("overlapping or orphaned play call")
			}
		})
	}
}
