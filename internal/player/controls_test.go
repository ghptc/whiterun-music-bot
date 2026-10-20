package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"whiterun/internal/media"
)

func TestQueueEditsModelAndMutation(t *testing.T) {
	// Detached player cannot consume positions while the reference model runs.
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer p.Close(context.Background())
	model := []int{}
	p.mu.Lock()
	for i := 0; i < 100; i++ {
		model = append(model, i)
		p.queue = append(p.queue, queueEntry{Track: media.Track{Title: fmt.Sprint(i), RequestedBy: "traveler", Artists: []string{"artist"}}, Pending: i%2 == 0, position: i, jobID: 7})
	}
	p.mu.Unlock()
	rng := rand.New(rand.NewPCG(55, 91))
	for len(model) > 0 {
		from, to := rng.IntN(len(model)), rng.IntN(len(model))
		if err := p.Move(from+1, to+1); err != nil {
			t.Fatal(err)
		}
		value := model[from]
		model = append(model[:from], model[from+1:]...)
		model = append(model, 0)
		copy(model[to+1:], model[to:])
		model[to] = value
		for i, want := range model {
			e := p.queue[i]
			if e.position != want || e.jobID != 7 || e.Pending != (want%2 == 0) || e.Track.Title != fmt.Sprint(want) || e.Track.RequestedBy != "traveler" {
				t.Fatalf("entry mutated at %d: %+v", i, e)
			}
		}
		snapshot := p.State()
		snapshot.Entries[0].Track.Artists[0] = "mutated"
		if p.queue[0].Track.Artists[0] != "artist" {
			t.Fatal("snapshot aliases entry")
		}
		before := p.State()
		if p.Remove(0) == nil || p.Move(1, len(model)+1) == nil || p.Jump(len(model)+1) == nil {
			t.Fatal("invalid position accepted")
		}
		if !reflect.DeepEqual(before, p.State()) {
			t.Fatal("invalid edit mutated state")
		}
		originalIDs := map[int]bool{}
		for _, id := range model {
			originalIDs[id] = true
		}
		if err := p.Shuffle(); err != nil {
			t.Fatal(err)
		}
		if len(p.queue) != len(model) {
			t.Fatal("shuffle changed length")
		}
		seen := map[int]bool{}
		model = nil
		for _, e := range p.queue {
			if !originalIDs[e.position] {
				t.Fatal("shuffle invented entry")
			}
			if seen[e.position] {
				t.Fatal("duplicate")
			}
			seen[e.position] = true
			model = append(model, e.position)
		}
		remove := rng.IntN(len(model))
		if err := p.Remove(remove + 1); err != nil {
			t.Fatal(err)
		}
		model = append(model[:remove], model[remove+1:]...)
	}
	if p.Remove(1) == nil || p.Move(1, 1) == nil || p.Jump(1) == nil || p.Shuffle() == nil || p.Replay() == nil {
		t.Fatal("empty queue accepted")
	}
}
func TestConcurrentQueueControls(t *testing.T) {
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer p.Close(context.Background())
	p.mu.Lock()
	for i := 0; i < 200; i++ {
		p.queue = append(p.queue, queueEntry{Track: media.Track{Title: fmt.Sprint(i)}})
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				_ = p.Move(1, 2)
				_ = p.Shuffle()
				_ = p.Remove(200)
				_ = p.SetLoop("queue")
				p.SetAutoplay(j%2 == 0)
				_ = p.Seek("1:30")
				_ = p.State()
			}
		})
	}
	wg.Wait()
	state := p.State()
	seen := map[string]bool{}
	if len(state.Entries) != 199 {
		t.Fatal(len(state.Entries))
	}
	for _, e := range state.Entries {
		if seen[e.Track.Title] {
			t.Fatal("duplicate")
		}
		seen[e.Track.Title] = true
	}
}
func TestPendingEditsKeepResolverIdentity(t *testing.T) {
	started := make(chan string, 4)
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error {
		started <- tr.Title
		<-ctx.Done()
		return ctx.Err()
	})
	add(t, p, "current")
	receive(t, started, "current")
	gate := make(chan struct{})
	job := begin(t, p, context.Background(), numberedTracks(1, 4), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
		select {
		case <-gate:
			return matched(tr), nil
		case <-ctx.Done():
			return media.Track{}, ctx.Err()
		}
	})
	if err := p.Move(4, 1); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(2); err != nil {
		t.Fatal(err)
	} // original position 1
	close(gate)
	jobDone(t, job)
	state := p.State()
	if len(state.Entries) != 3 || state.Entries[0].Track.Title != "4" {
		t.Fatal(state.Entries)
	}
	for _, e := range state.Entries {
		if e.Pending || e.Track.Title == "1" || e.Track.Position != e.Position {
			t.Fatal(e)
		}
	}
	if err := p.Jump(2); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "2")
}
func TestLoopReplaySkipAndFailures(t *testing.T) {
	for _, mode := range []string{"track", "queue"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan string, 16)
			finish := make(chan error)
			p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
				_ = v.WriteFrame(ctx, []byte{1})
				started <- tr.Title
				select {
				case err := <-finish:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err := p.SetLoop(mode); err != nil {
				t.Fatal(err)
			}
			add(t, p, "a")
			receive(t, started, "a")
			add(t, p, "b")
			finish <- nil
			if mode == "track" {
				receive(t, started, "a")
			} else {
				receive(t, started, "b")
			}
			if err := p.Replay(); err != nil {
				t.Fatal(err)
			}
			if mode == "track" {
				receive(t, started, "a")
			} else {
				receive(t, started, "b")
			}
			p.Skip()
			if mode == "track" {
				receive(t, started, "b")
			} else {
				receive(t, started, "a")
			}
			finish <- errors.New("unavailable")
			await(t, func() bool { return p.State().Current == nil })
			if p.Length() != 0 {
				t.Fatal("failed track repeated")
			}
			p.Stop()
			if p.State().Loop != "off" {
				t.Fatal("stop retained loop")
			}
		})
	}
}
func TestTimestampAndPausedSeek(t *testing.T) {
	for _, s := range []string{"1:30", "01:30", "0:01:30"} {
		if got, err := ParseTimestamp(s); err != nil || got != 90*time.Second {
			t.Fatal(s, got, err)
		}
	}
	for _, s := range []string{"-1:30", "1:60", "1", "1:2:3:4", "+1:00", "999999:00", "1::00"} {
		if _, err := ParseTimestamp(s); err == nil {
			t.Fatal(s)
		}
	}
	started := make(chan string, 1)
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error {
		started <- tr.Title
		<-ctx.Done()
		return ctx.Err()
	})
	if err := p.Enqueue(media.Track{Title: "a", Duration: time.Minute}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "a")
	_, _ = p.SetPaused(true)
	before := p.State()
	if p.Seek("0:30") == nil || p.Seek("1:30") == nil {
		t.Fatal("seek simulated success")
	}
	if !reflect.DeepEqual(before, p.State()) {
		t.Fatal("seek changed paused playback")
	}
}

func TestConcurrentReplaySkipNeverOverlapsStreams(t *testing.T) {
	var active, maximum atomic.Int32
	started := make(chan string, 100)
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		started <- tr.Title
		<-ctx.Done()
		time.Sleep(time.Millisecond) // Keep teardown open while other controls execute.
		return ctx.Err()
	})
	other, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	add(t, other, "other guild")
	await(t, func() bool { return other.State().Current != nil })
	before := other.State()
	add(t, p, "current")
	receive(t, started, "current")
	for i := 0; i < 30; i++ {
		add(t, p, fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() { _ = p.Replay(); p.Skip(); _ = p.Jump(1) })
	}
	wg.Wait()
	p.Stop()
	await(t, func() bool { return active.Load() == 0 })
	if maximum.Load() != 1 {
		t.Fatal("overlapping streams", maximum.Load())
	}
	if !reflect.DeepEqual(before, other.State()) {
		t.Fatal("other guild mutated")
	}
	for _, operation := range []func(){func() { p.Clear() }, func() { p.Leave(context.Background()) }} {
		_ = p.SetLoop("queue")
		operation()
		if p.State().Loop != "off" {
			t.Fatal("loop not disabled")
		}
	}
}
