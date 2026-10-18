package player

import (
	"context"
	"sync"
	"testing"
	"whiterun/internal/media"
)

func TestPreviousHistoryAndQueueOrder(t *testing.T) {
	started := make(chan string, 20)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	if p.Previous() == nil {
		t.Fatal("previous accepted without history")
	}
	add(t, p, "one")
	receive(t, started, "one")
	if p.Previous() == nil {
		t.Fatal("first song has no predecessor")
	}
	add(t, p, "two")
	add(t, p, "three")
	p.Skip()
	receive(t, started, "two")
	if _, err := p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := p.Previous(); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "one")
	state := p.State()
	if state.Paused || len(state.Queued) != 2 || state.Queued[0].Title != "two" || state.Queued[1].Title != "three" {
		t.Fatal(state)
	}
	p.Skip()
	receive(t, started, "two")
	p.Skip()
	receive(t, started, "three")
	p.Stop()
	if err := p.Previous(); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "three")
	history := p.History()
	history[0].Title = "mutated"
	if p.History()[0].Title == "mutated" {
		t.Fatal("aliased history")
	}
}
func TestPreviousConcurrentControls(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
		_ = v.WriteFrame(ctx, []byte{1})
		<-ctx.Done()
		return ctx.Err()
	})
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for range 100 {
				_ = p.Enqueue(media.Track{Title: "Song", Explicit: new(true)}, p.Ticket())
				_ = p.Previous()
				p.Skip()
				p.State()
				p.SetPaused(true)
				p.SetPaused(false)
			}
		})
	}
	wg.Wait()
	p.Stop()
}
