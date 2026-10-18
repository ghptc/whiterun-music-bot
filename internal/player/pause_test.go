package player

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
)

func TestPauseResumeSameTrackAndQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 4)
		frames := make(chan string, 4)
		step := make(chan struct{})
		p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
			started <- track.Title
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-step:
				}
				if _, err := waitPlayback(ctx); err != nil {
					return err
				}
				frames <- track.Title
			}
		})
		add(t, p, "one")
		receive(t, started, "one")
		add(t, p, "two")
		if changed, err := p.SetPaused(true); !changed || err != nil {
			t.Fatal(changed, err)
		}
		if changed, err := p.SetPaused(true); changed || err != nil {
			t.Fatal(changed, err)
		}
		step <- struct{}{}
		synctest.Wait()
		select {
		case <-frames:
			t.Fatal("sent frame while paused")
		default:
		}
		state := p.State()
		if !state.Paused || state.Current.Title != "one" || len(state.Queued) != 1 || state.Queued[0].Title != "two" {
			t.Fatal(state)
		}
		if changed, err := p.SetPaused(false); !changed || err != nil {
			t.Fatal(changed, err)
		}
		receive(t, frames, "one")
		if changed, err := p.SetPaused(false); changed || err != nil {
			t.Fatal(changed, err)
		}
		select {
		case <-started:
			t.Fatal("resume restarted track")
		default:
		}
	})
}

func TestCancellationWhilePaused(t *testing.T) {
	for _, action := range []string{"skip", "stop", "leave", "shutdown", "parent cancellation"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan string, 4)
				step := make(chan struct{})
				p, v := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
					started <- track.Title
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-step:
					}
					_, err := waitPlayback(ctx)
					return err
				})
				add(t, p, "one")
				receive(t, started, "one")
				add(t, p, "two")
				_, _ = p.SetPaused(true)
				step <- struct{}{}
				synctest.Wait()
				switch action {
				case "skip":
					p.Skip()
				case "stop":
					p.Stop()
				case "leave":
					p.Leave(context.Background())
				case "shutdown":
					p.Close(context.Background())
				case "parent cancellation":
					p.cancel()
				}
				synctest.Wait()
				state := p.State()
				if state.Paused {
					t.Fatal("retained pause")
				}
				if action == "skip" {
					receive(t, started, "two")
					if state.Current == nil || state.Current.Title != "two" {
						t.Fatal(state)
					}
				} else if state.Current != nil {
					t.Fatal("retained current")
				}
				if action == "leave" || action == "shutdown" {
					if !v.closed || len(state.Queued) != 0 {
						t.Fatal("retained session")
					}
				}
			})
		})
	}
}

func TestPauseIsolationAndConcurrentState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 2)
		play := func(ctx context.Context, t media.Track, _ Voice) error {
			started <- t.Title
			<-ctx.Done()
			return ctx.Err()
		}
		p, _ := newTestPlayer(t, play)
		other, _ := newTestPlayer(t, play)
		if _, err := p.SetPaused(true); err == nil {
			t.Fatal("paused idle")
		}
		add(t, p, "one")
		receive(t, started, "one")
		add(t, other, "two")
		receive(t, started, "two")
		_, _ = p.SetPaused(true)
		if other.State().Paused {
			t.Fatal("cross guild pause")
		}
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 100 {
					_, _ = p.SetPaused(true)
					p.State()
					_, _ = p.SetPaused(false)
				}
			})
		}
		wg.Wait()
	})
}

func TestPausedGateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gate := &pauseGate{}
	gate.set(true)
	cancel()
	if _, err := waitPlayback(context.WithValue(ctx, pauseKey{}, gate)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
