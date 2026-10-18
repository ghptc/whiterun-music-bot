package player

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

func TestNextReadyDoesNotWaitForDistantVerification(t *testing.T) {
	started := make(chan string, 123)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	var distant atomic.Int32
	j := begin(t, p, context.Background(), numberedTracks(1, 123), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
		if track.Position > 2 {
			distant.Add(1)
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		}
		return matched(track), nil
	})
	firstResult(t, j)
	receive(t, started, "1")
	await(t, func() bool { return len(p.State().Queued) == 1 && distant.Load() > 0 })
	p.Skip()
	receive(t, started, "2")
	if p.State().Current.Position != 2 {
		t.Fatal("lost playlist identity")
	}
	p.Stop()
	jobDone(t, j)
}

func TestCollectionPerPositionTimeoutAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 3)
		p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
			started <- track.Title
			<-ctx.Done()
			return ctx.Err()
		})
		var deadline atomic.Bool
		j := begin(t, p, context.Background(), numberedTracks(1, 3), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
			if track.Position == 2 {
				<-ctx.Done()
				deadline.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
				return media.Track{}, ctx.Err()
			}
			return matched(track), nil
		})
		firstResult(t, j)
		receive(t, started, "1")
		p.Skip()
		time.Sleep(CollectionResolveTimeout + time.Second)
		synctest.Wait()
		receive(t, started, "3")
		jobDone(t, j)
		if !deadline.Load() || j.progress.Failed != 1 {
			t.Fatalf("timeout did not advance: %+v", j.progress)
		}
	})
}

func Test123UnavailablePlaybackAttemptsTerminate(t *testing.T) {
	var attempts atomic.Int32
	p, _ := newTestPlayer(t, func(context.Context, media.Track, Voice) error {
		attempts.Add(1)
		return &StreamError{"youtube_auth_challenge", fmt.Errorf("authentication required")}
	})
	if err := p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return attempts.Load() == 123 && p.State().Current == nil })
	if p.Length() != 0 || p.State().Halted || p.State().ProviderBlocked {
		t.Fatal("unavailable queue stalled")
	}
}
