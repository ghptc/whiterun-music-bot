package player

import (
	"context"
	"errors"
	"testing"
	"whiterun/internal/media"
)

func TestStartedAttemptFailureIsInterrupted(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		return &StreamError{"audio_stall", errors.New("injected after first frame")}
	})
	starts := make(chan PlaybackEvent, 1)
	failures := make(chan PlaybackEvent, 1)
	p.SetOnStart(func(e PlaybackEvent) { starts <- e })
	p.SetOnSkip(func(e PlaybackEvent) { failures <- e })
	tr := media.Track{SpotifyID: "spotify-original", Title: "Little Bad Girl", Position: 8, Total: 123}
	if err := p.EnqueueMany([]media.Track{tr}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return len(starts) == 1 && len(failures) == 1 })
	started := <-starts
	failed := <-failures
	if started.State != Playing || failed.State != Failed || failed.FailureStage != "playing" || failed.Sequence != started.Sequence || failed.Track.SpotifyID != tr.SpotifyID || failed.Track.Position != 8 {
		t.Fatal(started, failed)
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
