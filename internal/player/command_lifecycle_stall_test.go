package player

import (
	"context"
	"testing"
	"testing/synctest"
	"whiterun/internal/media"
)

func qa013Production302(t *testing.T, loseNotification, stopWhileWaiting bool) {
	synctest.Test(t, func(t *testing.T) {
		eof, resolve := make(chan struct{}), make(chan struct{})
		p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			if tr.Position == 1 {
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
		j := begin(t, p, context.Background(), numberedTracks(1, 302), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			if tr.Position > 7 {
				select {
				case <-resolve:
				case <-ctx.Done():
					return media.Track{}, ctx.Err()
				}
			}
			return matched(tr), nil
		})
		firstResult(t, j)
		synctest.Wait()
		if loseNotification {
			// Fault injection reproduces the pre-OBS-001 notification path: change
			// permutation and notify playback only, leaving the coordinator asleep.
			p.mu.Lock()
			p.queue[0], p.queue[len(p.queue)-1] = p.queue[len(p.queue)-1], p.queue[0]
			select {
			case p.wake <- struct{}{}:
			default:
			}
			p.mu.Unlock()
		} else if err := p.shuffle(func(n int, swap func(int, int)) { swap(0, n-1) }); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		close(eof)
		synctest.Wait()
		p.mu.Lock()
		ready, pending := p.queueCountsLocked()
		headPending := p.queue[0].Pending
		p.mu.Unlock()
		if ready != 6 || pending != 295 || !headPending || p.State().Current != nil {
			t.Fatalf("production signature lost: ready=%d pending=%d headPending=%v current=%v", ready, pending, headPending, p.State().Current)
		}
		if stopWhileWaiting {
			p.Stop()
			synctest.Wait()
		}
		close(resolve)
		synctest.Wait()
		if stopWhileWaiting {
			if p.State().Current != nil || p.Length() != 0 {
				t.Fatal("resolution after stop resurrected playback")
			}
		} else {
			if s := p.State(); s.Current == nil || s.Current.Position != 302 {
				t.Fatal("shuffle + EOF did not advance after pending resolution", s.Current)
			}
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
		if p.State().Current != nil || p.Length() != 0 {
			t.Fatal("stop resurrected collection work")
		}
	})
}
func TestQA013Production302ShuffleEOF(t *testing.T) { qa013Production302(t, false, false) }
func TestQA013Production302LostNotificationAdvancesAfterEOF(t *testing.T) {
	qa013Production302(t, true, false)
}
func TestFIX014StopDuringEOFWaitDoesNotResurrect(t *testing.T) { qa013Production302(t, true, true) }
