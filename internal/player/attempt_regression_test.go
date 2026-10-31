package player

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"whiterun/internal/media"
)

func TestStartedAttemptStallRetainsConfirmedPosition(t *testing.T) {
	var calls atomic.Int32
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		if calls.Add(1) == 1 {
			return &StreamError{"audio_stall", errors.New("injected after first frame")}
		}
		<-ctx.Done()
		return ctx.Err()
	})
	var failures atomic.Int32
	p.SetOnSkip(func(PlaybackEvent) { failures.Add(1) })
	tr := media.Track{SpotifyID: "spotify-original", Title: "Little Bad Girl", Position: 8, Total: 123}
	if err := p.EnqueueMany([]media.Track{tr}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return calls.Load() == 2 && p.State().CurrentStarted })
	state := p.State()
	if failures.Load() != 0 || state.Current == nil || state.Current.SpotifyID != tr.SpotifyID || state.Current.Position != 8 || state.Current.ResumeOffset != 20*time.Millisecond {
		t.Fatal(state)
	}
}

func TestStaleStartCannotAnnounceCurrentAttempt(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	events := make(chan PlaybackEvent, 2)
	p.SetOnStart(func(e PlaybackEvent) { events <- e })
	if err := p.EnqueueMany([]media.Track{{Title: "current"}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return p.State().Current != nil })
	p.mu.Lock()
	sequence := p.sequence
	p.mu.Unlock()
	p.announceStart(context.Background(), media.Track{Title: "obsolete"}, sequence-1)
	select {
	case e := <-events:
		t.Fatal("stale callback announced", e)
	default:
	}
	p.announceStart(context.Background(), media.Track{Title: "current"}, sequence)
	p.announceStart(context.Background(), media.Track{Title: "current"}, sequence)
	<-events
	select {
	case e := <-events:
		t.Fatal("duplicate callback announced", e)
	default:
	}
}

func TestCollectionCannotCommitAnotherSpotifyIdentity(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	tracks := []media.Track{{SpotifyID: "original-one", Title: "one"}, {SpotifyID: "original-two", Title: "two"}}
	j := begin(t, p, context.Background(), tracks, func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
		result := matched(tr)
		if tr.SpotifyID == "original-one" {
			result.SpotifyID = "stale-other-track"
		}
		return result, nil
	})
	first := firstResult(t, j)
	if first.Track.SpotifyID != "original-two" || first.Track.Position != 2 {
		t.Fatal(first)
	}
	jobDone(t, j)
}
