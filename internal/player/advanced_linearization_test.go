package player

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
)

func TestAdvancedConcurrentQueueLinearization(t *testing.T) {
	permutations := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for seed := uint64(0); seed < 128; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := make(chan struct{})
				p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				})
				if err := p.Enqueue(advancedTrack("sentinel"), p.Ticket()); err != nil {
					t.Fatal(err)
				}
				<-started
				initial := []string{"a", "b", "c"}
				for _, id := range initial {
					if err := p.Enqueue(advancedTrack(id), p.Ticket()); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				rng := rand.New(rand.NewPCG(seed, 8))
				from, to, remove := rng.IntN(5), rng.IntN(5), rng.IntN(5)
				gate := make(chan struct{})
				var failures [3]bool
				var wg sync.WaitGroup
				actions := []func() error{
					func() error { return p.EnqueueMany([]media.Track{advancedTrack("d"), advancedTrack("e")}, p.Ticket()) },
					func() error { return p.Move(from, to) },
					func() error { return p.Remove(remove) },
				}
				for i, action := range actions {
					wg.Go(func() { <-gate; failures[i] = action() != nil })
				}
				close(gate)
				wg.Wait()
				synctest.Wait()
				observed := advancedIDs(p.State().Queued)
				matched := false
				for _, order := range permutations {
					model := slices.Clone(initial)
					var expected [3]bool
					for _, op := range order {
						switch op {
						case 0:
							model = append(model, "d", "e")
						case 1:
							if from < 1 || to < 1 || from > len(model) || to > len(model) {
								expected[1] = true
								continue
							}
							id := model[from-1]
							rest := append(slices.Clone(model[:from-1]), model[from:]...)
							model = append(slices.Clone(rest[:to-1]), id)
							model = append(model, rest[to-1:]...)
						case 2:
							if remove < 1 || remove > len(model) {
								expected[2] = true
								continue
							}
							model = append(model[:remove-1], model[remove:]...)
						}
					}
					if slices.Equal(model, observed) && expected == failures {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("non-linearizable seed=%d move(%d,%d) remove(%d) state=%v errors=%v", seed, from, to, remove, observed, failures)
				}
				s := p.State()
				if s.Current == nil || s.Current.Title != "sentinel" {
					t.Fatal("queue edits disturbed active stream")
				}
			})
		})
	}
}
