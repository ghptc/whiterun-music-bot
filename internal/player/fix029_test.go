package player

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
	"whiterun/internal/media"
)

// A clean but prematurely exhausted source remains a failure. It must not be
// fed back into network recovery ahead of independently queued occurrences.
func TestFIX029PrematureEOFAdvancesOnceWithoutVoiceLeave(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{})
	failures := make(chan PlaybackEvent, 4)
	p, v := newTestPlayer(t, func(ctx context.Context, track media.Track, voice Voice) error {
		if err := voice.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		started <- strconv.Itoa(track.Position)
		if track.Position == 1 {
			return &StreamError{"audio_eof", errors.New("clean source ended before metadata duration")}
		}
		if track.Position == 2 {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		<-ctx.Done()
		return ctx.Err()
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("player snapshot after failure: %+v", p.State())
		}
	})
	p.SetOnSkip(func(e PlaybackEvent) { failures <- e })
	track := media.Track{Title: "same recording", Duration: time.Minute}
	if err := p.EnqueueMany([]media.Track{track, track, track}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "1")
	receive(t, started, "2")
	state := p.State()
	if state.Halted || state.Current == nil || state.Current.Position != 2 || len(state.Queued) != 1 || state.Queued[0].Position != 3 {
		t.Fatalf("queue did not advance exactly once: %+v", state)
	}
	close(release)
	receive(t, started, "3")
	select {
	case e := <-failures:
		if e.State != Failed || e.Track.Position != 1 {
			t.Fatalf("premature EOF lost failure classification: %+v", e)
		}
	default:
		t.Fatal("missing terminal failure notification")
	}
	if len(failures) != 0 || len(started) != 0 {
		t.Fatal("duplicate completion or advancement")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		t.Fatal("source EOF left Voice")
	}
}
