package player

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whiterun/internal/media"
)

func numberedTracks(first, last int) []media.Track {
	tracks := make([]media.Track, 0, last-first+1)
	for i := first; i <= last; i++ {
		tracks = append(tracks, media.Track{Title: fmt.Sprint(i), Artists: []string{"Artist"}})
	}
	return tracks
}
func TestBulkSkipPositions(t *testing.T) {
	for _, count := range []int{0, 1, 2, 50, 123, 1001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			started := make(chan string, 125)
			cancelled := make(chan string, 125)
			release := make(chan struct{})
			p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
				started <- track.Title
				<-ctx.Done()
				cancelled <- track.Title
				if track.Title == "1" {
					<-release
				}
				return ctx.Err()
			})
			defer close(release)
			if err := p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
				t.Fatal(err)
			}
			receive(t, started, "1")
			calls := 0
			p.mu.Lock()
			cancel := p.trackCancel
			p.trackCancel = func() { calls++; cancel() }
			p.mu.Unlock()
			actual := count
			if count == 0 {
				actual = 1
				if !p.Skip() {
					t.Fatal("default skip failed")
				}
			} else {
				result, err := p.SkipN(count)
				if err != nil || result.Skipped != min(count, 123) || result.Remaining != max(0, 123-count) {
					t.Fatal(result, err)
				}
				if count < 123 && (result.Next == nil || result.Next.Title != fmt.Sprint(count+1)) {
					t.Fatal(result)
				}
			}
			receive(t, cancelled, "1")
			if calls != 1 {
				t.Fatal("playback canceled more than once", calls)
			}
			state := p.State()
			if state.Current != nil || state.Paused || len(state.Queued) != max(0, 123-actual) {
				t.Fatal("stale logical current")
			}
			for i, track := range state.Queued {
				if track.Title != fmt.Sprint(actual+i+1) {
					t.Fatal("prefix/order incorrect")
				}
			}
			select {
			case track := <-started:
				t.Fatal("intermediate playback", track)
			default:
			}
			// Permit teardown only after checking the atomic mutation.
			release <- struct{}{}
			if actual < 123 {
				receive(t, started, fmt.Sprint(actual+1))
				state = p.State()
				if state.Current.Title != fmt.Sprint(actual+1) || len(state.Queued) != 122-actual {
					t.Fatal("wrong successor")
				}
				for i, track := range state.Queued {
					if track.Title != fmt.Sprint(actual+i+2) {
						t.Fatal("remaining order")
					}
				}
			}
		})
	}
}
func TestBulkSkipInvalidEmptyAndStarting(t *testing.T) {
	p := &GuildPlayer{queue: queueEntries(numberedTracks(1, 3)), wake: make(chan struct{}, 1)}
	for _, count := range []int{-50, 0, MaxSkipTracks + 1, int(^uint(0) >> 1)} {
		if result, err := p.SkipN(count); err == nil || result.Skipped != 0 || p.Length() != 3 {
			t.Fatal("invalid skip mutated queue")
		}
	}
	result, err := p.SkipN(2)
	if err != nil || result.Skipped != 2 || result.Next.Title != "3" {
		t.Fatal(result, err)
	}
	result, err = p.SkipN(2)
	if err != nil || result.Skipped != 1 || result.Next != nil {
		t.Fatal(result, err)
	}
	if p.Skip() {
		t.Fatal("empty skip succeeded")
	}
}
func TestBulkSkipPaused(t *testing.T) {
	started := make(chan string, 3)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		_, err := waitPlayback(ctx)
		return err
	})
	if err := p.EnqueueMany(numberedTracks(1, 3), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "1")
	if _, err := p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	result, err := p.SkipN(2)
	if err != nil || result.Skipped != 2 {
		t.Fatal(result, err)
	}
	receive(t, started, "3")
	if p.State().Paused {
		t.Fatal("successor stayed paused")
	}
}
func TestConcurrentBulkSkipDoesNotRecountCancelledTrack(t *testing.T) {
	started := make(chan string, 30)
	release := make(chan struct{})
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		if track.Title == "1" {
			<-release
		}
		return ctx.Err()
	})
	defer close(release)
	if err := p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "1")
	var calls atomic.Int32
	p.mu.Lock()
	cancel := p.trackCancel
	p.trackCancel = func() { calls.Add(1); cancel() }
	p.mu.Unlock()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.SkipN(10)
			if err != nil || r.Skipped != 10 {
				t.Error(r, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate cancellation")
	}
	state := p.State()
	if state.Current != nil || len(state.Queued) != 73 || state.Queued[0].Title != "51" {
		t.Fatal("concurrent skip lost positions")
	}
	release <- struct{}{}
	receive(t, started, "51")
}
func TestBulkSkipStopClearEnqueueRaces(t *testing.T) {
	for _, operation := range []string{"stop", "clear", "enqueue"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan string, 130)
			release := make(chan struct{})
			p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
				started <- track.Title
				<-ctx.Done()
				if track.Title == "1" {
					<-release
				}
				return ctx.Err()
			})
			defer close(release)
			if err := p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
				t.Fatal(err)
			}
			receive(t, started, "1")
			var calls atomic.Int32
			p.mu.Lock()
			cancel := p.trackCancel
			p.trackCancel = func() { calls.Add(1); cancel() }
			p.mu.Unlock()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = p.SkipN(50) }()
			go func() {
				defer wg.Done()
				switch operation {
				case "stop":
					p.Stop()
				case "clear":
					p.Clear()
				case "enqueue":
					if err := p.EnqueueMany(numberedTracks(124, 125), p.Ticket()); err != nil {
						t.Error(err)
					}
				}
			}()
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatal("cancellation duplicated")
			}
			state := p.State()
			if operation == "enqueue" {
				if len(state.Queued) != 75 || state.Queued[0].Title != "51" || state.Queued[74].Title != "125" {
					t.Fatal("enqueue/skip order")
				}
			} else if state.Current != nil || len(state.Queued) != 0 {
				t.Fatal("stop/clear left queued tracks")
			}
			release <- struct{}{}
			if operation == "enqueue" {
				receive(t, started, "51")
			}
		})
	}
}
func TestSnapshotCopiesMetadataAndConsistentBatch(t *testing.T) {
	p := &GuildPlayer{current: &media.Track{Title: "current", Artists: []string{"current artist"}}, queue: queueEntries(numberedTracks(2, 123)), wake: make(chan struct{}, 1)}
	state := p.State()
	state.Current.Artists[0] = "changed"
	state.Queued[0].Artists[0] = "changed"
	if got := p.State(); got.Current.Artists[0] != "current artist" || got.Queued[0].Artists[0] != "Artist" {
		t.Fatal("snapshot aliases metadata")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = p.SkipN(50) }()
	for range 100 {
		snapshot := p.State()
		if snapshot.Current != nil {
			if len(snapshot.Queued) != 122 || snapshot.Queued[0].Title != "2" {
				t.Error("torn pre-skip snapshot")
			}
		} else if len(snapshot.Queued) != 73 || snapshot.Queued[0].Title != "51" {
			t.Error("torn post-skip snapshot")
		}
	}
	wg.Wait()
}
func TestBulkSkipDuringPlaybackStartup(t *testing.T) {
	entered := make(chan struct{})
	next := make(chan string, 1)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		if track.Title == "1" {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		next <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	if err := p.EnqueueMany(numberedTracks(1, 3), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("startup never entered")
	}
	if _, err := p.SkipN(2); err != nil {
		t.Fatal(err)
	}
	receive(t, next, "3")
}

func queueEntries(tracks []media.Track) []queueEntry {
	entries := make([]queueEntry, len(tracks))
	for i, t := range tracks {
		entries[i] = queueEntry{Track: t}
	}
	return entries
}
