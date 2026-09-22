package player

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"whiterun/internal/media"
)

type fakeVoice struct {
	closed bool
	mu     sync.Mutex
}

func (*fakeVoice) WriteFrame(context.Context, []byte) error { return nil }
func (*fakeVoice) Speaking(context.Context, bool) error     { return nil }
func (v *fakeVoice) Close(context.Context)                  { v.mu.Lock(); v.closed = true; v.mu.Unlock() }
func receive(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("waiting for %s", want)
	}
}
func newTestPlayer(t *testing.T, play PlayFunc) (*GuildPlayer, *fakeVoice) {
	t.Helper()
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), play)
	v := &fakeVoice{}
	if err := p.Attach(v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close(context.Background()) })
	return p, v
}
func add(t *testing.T, p *GuildPlayer, title string) {
	t.Helper()
	if err := p.Enqueue(media.Track{Title: title}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
}
func TestQueueAdvanceSkipFailureAndStop(t *testing.T) {
	started := make(chan string, 10)
	release := make(chan error, 10)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		select {
		case err := <-release:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	add(t, p, "one")
	receive(t, started, "one")
	add(t, p, "two")
	add(t, p, "three")
	current, queue := p.Snapshot()
	if current.Title != "one" || len(queue) != 2 || queue[0].Title != "two" {
		t.Fatal("wrong snapshot")
	}
	current.Title = "mutated"
	queue[0].Title = "mutated"
	current, queue = p.Snapshot()
	if current.Title != "one" || queue[0].Title != "two" {
		t.Fatal("snapshot aliases state")
	}
	release <- nil
	receive(t, started, "two") // normal completion
	release <- errors.New("FFmpeg failed")
	receive(t, started, "three") // recover from failure
	add(t, p, "four")
	if !p.Skip() {
		t.Fatal("skip failed")
	}
	receive(t, started, "four")
	add(t, p, "must not play")
	ticket := p.Ticket()
	p.Stop()
	if err := p.Enqueue(media.Track{Title: "stale search"}, ticket); err == nil {
		t.Fatal("stop accepted stale search")
	}
	add(t, p, "after stop")
	receive(t, started, "after stop")
	_, queue = p.Snapshot()
	if len(queue) != 0 {
		t.Fatal("stop did not clear queue")
	}
}
func TestLeaveAndGuildIsolation(t *testing.T) {
	first := make(chan string, 5)
	second := make(chan string, 5)
	fn := func(ch chan string) PlayFunc {
		return func(ctx context.Context, track media.Track, _ Voice) error {
			ch <- track.Title
			<-ctx.Done()
			return ctx.Err()
		}
	}
	p, v := newTestPlayer(t, fn(first))
	other, _ := newTestPlayer(t, fn(second))
	add(t, p, "first guild")
	add(t, other, "other guild")
	receive(t, first, "first guild")
	receive(t, second, "other guild")
	add(t, p, "discard")
	ticket := p.Ticket()
	p.Leave(context.Background())
	if !v.closed {
		t.Fatal("voice not closed")
	}
	current, queue := p.Snapshot()
	if current != nil || len(queue) != 0 {
		t.Fatal("leave retained state")
	}
	current, _ = other.Snapshot()
	if current == nil || current.Title != "other guild" {
		t.Fatal("leave affected another guild")
	}
	if err := p.Enqueue(media.Track{Title: "stale"}, ticket); err == nil {
		t.Fatal("enqueued after leave")
	}
	if err := p.Attach(&fakeVoice{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(media.Track{Title: "stale"}, ticket); err == nil {
		t.Fatal("stale search survived resummon")
	}
	add(t, p, "resummoned")
	receive(t, first, "resummoned")
}
func TestConcurrentQueueAccess(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				ticket := p.Ticket()
				_ = p.Enqueue(media.Track{Title: "song"}, ticket)
				p.Snapshot()
				p.Clear()
				p.Length()
				_ = p.EnqueueMany([]media.Track{{Title: "batch one"}, {Title: "batch two"}}, ticket)
				p.Skip()
				p.Stop()
			}
		}()
	}
	wg.Wait()
	p.Stop()
}

func TestBatchOrderClearAndCapacity(t *testing.T) {
	started := make(chan string, 10)
	release := make(chan error, 10)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		select {
		case err := <-release:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	batch := []media.Track{{Title: "one"}, {Title: "two"}, {Title: "three"}}
	if err := p.EnqueueMany(batch, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	batch[1].Title = "mutated"
	receive(t, started, "one")
	if p.Length() != 2 {
		t.Fatal(p.Length())
	}
	release <- nil
	receive(t, started, "two")
	if n := p.Clear(); n != 1 {
		t.Fatal(n)
	}
	current, queue := p.Snapshot()
	if current == nil || current.Title != "two" || len(queue) != 0 || p.Clear() != 0 {
		t.Fatal("clear interrupted playback or retained queue")
	}
	select {
	case next := <-started:
		t.Fatalf("unexpected playback %s", next)
	default:
	}
	ticket := p.Ticket()
	if err := p.EnqueueMany(make([]media.Track, 1000), ticket); err != nil {
		t.Fatal(err)
	}
	if err := p.EnqueueMany([]media.Track{{Title: "overflow"}}, ticket); err == nil || p.Length() != 1000 {
		t.Fatal("capacity was not atomic")
	}
	p.Stop()
	if err := p.EnqueueMany(batch, ticket); err == nil {
		t.Fatal("stale batch accepted")
	}
}
