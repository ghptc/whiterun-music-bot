package player

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
)

func TestHardeningSeekValidationPreservesPlayback(t *testing.T) {
	for _, tc := range []struct {
		name             string
		duration         time.Duration
		position, reason string
	}{
		{"unknown start", 0, "0:00", "unavailable"},
		{"unknown position", 0, "1:00", "unavailable"},
		{"at end", time.Minute, "1:00", "unavailable"},
		{"past end", time.Minute, "1:01", "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := qualityDetached(t)
			p.mu.Lock()
			p.current = &media.Track{Title: "recording", Duration: tc.duration}
			p.pause = &pauseGate{}
			p.mu.Unlock()
			before := p.State()
			err := p.Seek(tc.position)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("seek classified incorrectly: %v", err)
			}
			if !reflect.DeepEqual(before, p.State()) {
				t.Fatal("unsupported seek mutated playback")
			}
		})
	}
}

// Distinct, accepted imports must remain ordered even when their workers block.
func TestHardeningConcurrentImportSnapshotOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		matcher := func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		}
		jobs := make([]*CollectionJob, 8)
		for i := range jobs {
			jobs[i] = begin(t, p, context.Background(), []media.Track{{Title: "pending"}}, matcher)
		}
		synctest.Wait()
		for repeat := 0; repeat < 20; repeat++ {
			state := p.State()
			if len(state.Resolving) != len(jobs) {
				t.Fatalf("imports lost: %d", len(state.Resolving))
			}
			for i, progress := range state.Resolving {
				if progress.ID != uint64(i+1) {
					t.Fatalf("unordered or duplicated import: %+v", state.Resolving)
				}
			}
		}
		p.Clear()
		for _, job := range jobs {
			<-job.Done
		}
		synctest.Wait()
		if len(p.State().Resolving) != 0 || p.Length() != 0 {
			t.Fatal("cancelled imports retained")
		}
	})
}

func TestHardeningTimestampAcceptedBoundaries(t *testing.T) {
	for raw, want := range map[string]time.Duration{"00:00": 0, "09:59": 599 * time.Second, "60:00": time.Hour, "000001:00": time.Minute, "24:00:00": 24 * time.Hour} {
		t.Run(raw, func(t *testing.T) {
			got, err := ParseTimestamp(raw)
			if err != nil || got != want {
				t.Fatalf("valid timestamp: %v %v", got, err)
			}
		})
	}
}
func TestHardeningSkipMaximumCapacity(t *testing.T) {
	p := qualityDetached(t)
	result, err := p.SkipN(MaxSkipTracks)
	if err != nil || result.Skipped != 0 {
		t.Fatal(result, err)
	}
	if _, err := p.SkipN(MaxSkipTracks + 1); err == nil {
		t.Fatal("unbounded skip accepted")
	}
}
func TestHardeningDurableCapacityAndEmptyHistory(t *testing.T) {
	for _, size := range []int{0, MaxQueueTracks + 1, MaxQueueTracks + 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := qualityDetached(t)
			current := media.Track{Title: "current"}
			tracks := make([]media.Track, size)
			store := &qualityStore{state: DurableState{Queue: tracks}}
			if size == 0 {
				store.state.Current = &current
				store.state.CurrentStarted = true
			}
			err := p.ConfigureStore(store, "guild")
			if size > MaxQueueTracks+1 {
				if err == nil {
					t.Fatal("oversized recovery accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := size
			if size == 0 {
				want = 1
			}
			if p.Length() != want || len(p.History()) != 0 {
				t.Fatal("recovery lost queue or invented history")
			}
		})
	}
}
func TestHardeningPreviousFillsQueueCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		p.mu.Lock()
		p.queue = make([]queueEntry, MaxQueueTracks-1)
		p.history = []media.Track{{Title: "past", VideoID: "abcdefghijk"}}
		p.halted = true
		p.mu.Unlock()
		if err := p.Previous(); err != nil {
			t.Fatal("last queue slot rejected", err)
		}
		p.mu.Lock()
		count := len(p.queue)
		if p.current != nil {
			count++
		}
		p.mu.Unlock()
		if count != MaxQueueTracks {
			t.Fatalf("lost previous or queue: %d", count)
		}
		p.Stop()
		synctest.Wait()
	})
}

func TestHardeningReplayRejectsFullQueue(t *testing.T) {
	p := qualityDetached(t)
	p.current = &media.Track{Title: "current"}
	p.queue = make([]queueEntry, MaxQueueTracks)
	before := p.State()
	if err := p.Replay(); err == nil {
		t.Fatal("replay exceeded queue capacity")
	}
	if !reflect.DeepEqual(before, p.State()) {
		t.Fatal("rejected replay changed queue or current")
	}
}
