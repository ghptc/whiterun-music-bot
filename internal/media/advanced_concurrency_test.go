package media

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAdvancedSharedSearchCancellationSchedules(t *testing.T) {
	for _, survivors := range []int{0, 1, 10, 20} {
		t.Run(fmt.Sprint(survivors), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := &SearchCache{}
				release := make(chan struct{})
				stopped := make(chan struct{})
				var calls atomic.Int32
				work := func(ctx context.Context) (Track, error) {
					calls.Add(1)
					defer close(stopped)
					select {
					case <-release:
						return Track{Title: "shared", Artists: []string{"artist"}}, nil
					case <-ctx.Done():
						return Track{}, ctx.Err()
					}
				}
				cancels := make([]context.CancelFunc, 20)
				results := make([]chan error, 20)
				for i := range 20 {
					ctx, cancel := context.WithCancel(context.Background())
					cancels[i] = cancel
					results[i] = make(chan error, 1)
					go func() {
						tr, err := cache.resolve(ctx, "shared", work)
						if err == nil && tr.Title != "shared" {
							err = errors.New("identity lost")
						}
						results[i] <- err
					}()
				}
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatal("search not coalesced", calls.Load())
				}
				for i := 0; i < 20-survivors; i++ {
					cancels[i]()
				}
				synctest.Wait()
				if survivors > 0 {
					select {
					case <-stopped:
						t.Fatal("one guild canceled another guild's search")
					default:
					}
					close(release)
				}
				synctest.Wait()
				for i, result := range results {
					err := <-result
					if i < 20-survivors {
						if !errors.Is(err, context.Canceled) {
							t.Fatal("cancellation lost", i, err)
						}
					} else if err != nil {
						t.Fatal("survivor failed", i, err)
					}
					cancels[i]()
				}
				synctest.Wait()
				select {
				case <-stopped:
				default:
					t.Fatal("shared work leaked")
				}
				_, hit := cache.get("shared", time.Now())
				if hit != (survivors > 0) {
					t.Fatal("failure cached or success lost")
				}
			})
		})
	}
}
