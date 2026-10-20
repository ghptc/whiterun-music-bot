package player

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"

	"whiterun/internal/media"
)

func qualityDetached(t *testing.T) *GuildPlayer {
	t.Helper()
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	t.Cleanup(func() { p.Close(context.Background()) })
	return p
}

// A detached player cannot consume entries. The oracle uses independent slices
// of complete tracks, including metadata, rather than the player's entry helpers.
func TestQualityQueueModel(t *testing.T) {
	for _, size := range []int{1, 2, 17, 500, 1000} {
		for _, seed := range []uint64{7, 41, 20261019} {
			t.Run(fmt.Sprintf("size=%d/seed=%d", size, seed), func(t *testing.T) {
				p := qualityDetached(t)
				model := make([]media.Track, size)
				p.mu.Lock()
				for i := range model {
					model[i] = media.Track{Title: fmt.Sprint(i), Artists: []string{"artist", fmt.Sprint(i)}, RequestedBy: "listener", SpotifyID: fmt.Sprint(i), Position: i + 1, Total: size, Explicit: new(i%2 == 0)}
					p.queue = append(p.queue, queueEntry{Track: media.CloneTrack(model[i])})
				}
				p.mu.Unlock()
				rng := rand.New(rand.NewPCG(seed, seed+1))
				for step := 0; step < 200 && len(model) > 0; step++ {
					n := len(model)
					switch rng.IntN(5) {
					case 0:
						a, b := rng.IntN(n), rng.IntN(n)
						if err := p.Move(a+1, b+1); err != nil {
							t.Fatal(err)
						}
						tr := model[a]
						model = append(model[:a], model[a+1:]...)
						next := make([]media.Track, 0, n)
						next = append(next, model[:b]...)
						next = append(next, tr)
						next = append(next, model[b:]...)
						model = next
					case 1:
						i := rng.IntN(n)
						if err := p.Remove(i + 1); err != nil {
							t.Fatal(err)
						}
						model = append(model[:i], model[i+1:]...)
					case 2:
						count := rng.IntN(n) + 1
						got, err := p.SkipN(count)
						if err != nil || got.Skipped != count || got.Remaining != n-count {
							t.Fatalf("skip=%+v err=%v", got, err)
						}
						model = model[count:]
					case 3:
						i := rng.IntN(n)
						if err := p.Jump(i + 1); err != nil {
							t.Fatal(err)
						}
						model = model[i:]
					case 4:
						before := p.State()
						if p.Move(0, 1) == nil || p.Remove(n+1) == nil || p.Jump(-1) == nil {
							t.Fatal("invalid edit accepted")
						}
						if !reflect.DeepEqual(before, p.State()) {
							t.Fatal("invalid edit changed state")
						}
					}
					got := p.State().Queued
					if len(got) != len(model) {
						t.Fatalf("step %d length %d want %d", step, len(got), len(model))
					}
					for i := range got {
						if !reflect.DeepEqual(got[i], model[i]) {
							t.Fatalf("step %d position %d metadata/order changed", step, i)
						}
					}
					if len(got) > 0 {
						got[0].Artists[0] = "mutated"
						*got[0].Explicit = !*got[0].Explicit
						if !reflect.DeepEqual(p.State().Queued[0], model[0]) {
							t.Fatal("snapshot aliases player")
						}
					}
				}
			})
		}
	}
}

func TestQualityTwentyProducers(t *testing.T) {
	// Hold the active stream using the production test harness; all following
	// batches must be atomic even when twenty users enter together.
	started := make(chan string, 1)
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, _ Voice) error {
		started <- tr.Title
		<-ctx.Done()
		return ctx.Err()
	})
	add(t, p, "sentinel")
	receive(t, started, "sentinel")
	ticket := p.Ticket()
	gate := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for user := range 20 {
		wg.Go(func() {
			<-gate
			tracks := make([]media.Track, 50)
			for i := range tracks {
				tracks[i] = media.Track{Title: fmt.Sprintf("%d/%d", user, i), RequestedBy: fmt.Sprint(user), Artists: []string{"artist"}}
			}
			errs <- p.EnqueueMany(tracks, ticket)
		})
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state := p.State()
	if len(state.Queued) != 1000 {
		t.Fatal(len(state.Queued))
	}
	seen := map[string]bool{}
	for start := 0; start < 1000; start += 50 {
		owner := state.Queued[start].RequestedBy
		if seen[owner] {
			t.Fatal("duplicate producer", owner)
		}
		seen[owner] = true
		for i := range 50 {
			tr := state.Queued[start+i]
			if tr.Title != fmt.Sprintf("%s/%d", owner, i) || tr.RequestedBy != owner || tr.Position != i+1 || tr.Total != 50 {
				t.Fatalf("interleaved batch at %d: %+v", start+i, tr)
			}
		}
	}
	before := p.State()
	if p.Enqueue(media.Track{Title: "overflow"}, ticket) == nil {
		t.Fatal("capacity overflow accepted")
	}
	if !reflect.DeepEqual(before, p.State()) {
		t.Fatal("rejected insertion changed queue")
	}
	p.Stop()
	if p.Enqueue(media.Track{Title: "stale"}, ticket) == nil {
		t.Fatal("stop accepted obsolete search ticket")
	}
}
