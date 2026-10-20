package player

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"whiterun/internal/media"
)

func TestReorderedPendingHeadWakesSleepingResolver(t *testing.T) {
	for _, action := range []string{"move", "shuffle"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				eof := make(chan struct{})
				p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
					if track.Position == 1 {
						select {
						case <-eof:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					<-ctx.Done()
					return ctx.Err()
				})
				j := begin(t, p, context.Background(), numberedTracks(1, 14), func(_ context.Context, track media.Track, _ bool) (media.Track, error) { return matched(track), nil })
				firstResult(t, j)
				synctest.Wait()
				reorder := func() error { return p.Move(p.Length(), 1) }
				if action == "shuffle" {
					reorder = func() error { return p.shuffle(func(n int, swap func(int, int)) { swap(0, n-1) }) }
				}
				if err := reorder(); err != nil {
					t.Fatal(err)
				}
				close(eof)
				synctest.Wait()
				if s := p.State(); s.Current == nil || s.Current.Position != 14 {
					t.Errorf("reordered pending head stalled after EOF: current=%v", s.Current)
				}
			})
		})
	}
}

func TestShuffleWhileResolutionActiveKeepsSlotIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		j := begin(t, p, context.Background(), numberedTracks(1, 14), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
			if track.Position > 1 {
				select {
				case <-release:
				case <-ctx.Done():
					return media.Track{}, ctx.Err()
				}
			}
			return matched(track), nil
		})
		firstResult(t, j)
		synctest.Wait()
		p.mu.Lock()
		active := 0
		for _, search := range j.searches {
			if search != nil {
				active++
			}
		}
		p.mu.Unlock()
		if active == 0 {
			t.Fatal("fixture has no active resolution")
		}
		if err := p.shuffle(func(n int, swap func(int, int)) { swap(0, n-1) }); err != nil {
			t.Fatal(err)
		}
		unblock()
		synctest.Wait()
		state := p.State()
		if len(state.Entries) == 0 || state.Entries[0].Position != 14 || state.Entries[0].Pending {
			t.Fatal("shuffled pending head did not commit")
		}
		seen := map[int]bool{}
		for _, entry := range state.Entries {
			if entry.CollectionID != j.progress.ID || entry.Position != entry.Track.Position || seen[entry.Position] {
				t.Fatal("slot identity changed", entry)
			}
			seen[entry.Position] = true
		}
	})
}
