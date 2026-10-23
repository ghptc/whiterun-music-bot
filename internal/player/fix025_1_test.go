package player

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
)

// A virtual 3h session exercises the scheduler/watchdog over transitions and
// media failures. The invariant is checked before cleanup legitimately leaves.
func TestFIX0251ThreeHoursWithoutVoluntaryLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var completed atomic.Int32
		p, v := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
			time.Sleep(10 * time.Minute)
			completed.Add(1)
			switch tr.Position {
			case 2:
				return &StreamError{"ffmpeg_exit", errors.New("fixture decoder failure")}
			case 4:
				return &StreamError{"yt_dlp_exit", errors.New("fixture source failure")}
			}
			return v.WriteFrame(ctx, []byte{1})
		})
		tracks := make([]media.Track, 20)
		for i := range tracks {
			tracks[i] = media.Track{Title: fmt.Sprint(i), Position: i + 1}
		}
		if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for range 18 {
			time.Sleep(10 * time.Minute)
			synctest.Wait()
			v.mu.Lock()
			closed := v.closed
			v.mu.Unlock()
			if closed {
				t.Fatal("scheduler or watchdog voluntarily closed active Voice")
			}
		}
		if completed.Load() < 18 {
			t.Fatal("transitions did not continue", completed.Load())
		}
		s := p.State()
		if s.Current == nil || len(s.Queued) == 0 {
			t.Fatal("expected active playback and pending queue", s)
		}
	})
}

func TestFIX0251CloseOriginPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	derived := VoiceCloseContext(ctx, "command_leave")
	if VoiceCloseOrigin(derived) != "command_leave" || VoiceCloseOrigin(ctx) != "unattributed_close" {
		t.Fatal("lost logical origin")
	}
	cancel()
	if derived.Err() != context.Canceled {
		t.Fatal("origin masked cancellation")
	}
}

func TestFIX0251ResolverFailurePreservesActiveVoice(t *testing.T) {
	for _, failure := range []string{"spotify_unavailable", "resolution_timeout"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, v := newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				})
				add(t, p, "active")
				synctest.Wait()
				job := begin(t, p, context.Background(), numberedTracks(1, 3), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
					if tr.Position == 2 {
						if failure == "resolution_timeout" {
							bounded, cancel := context.WithTimeout(ctx, time.Second)
							defer cancel()
							<-bounded.Done()
							return media.Track{}, bounded.Err()
						}
						return media.Track{}, errors.New("Spotify temporarily unavailable")
					}
					return matched(tr), nil
				})
				synctest.Wait()
				time.Sleep(2 * time.Second)
				synctest.Wait()
				select {
				case <-job.Done:
				default:
					t.Fatal("resolver failed to finish")
				}
				v.mu.Lock()
				closed := v.closed
				v.mu.Unlock()
				s := p.State()
				if closed || s.VoiceSuspended || s.Current == nil || s.Current.Title != "active" || len(s.Queued) != 2 {
					t.Fatal("resolver disrupted voice or queue", s)
				}
			})
		})
	}
}

func TestFIX0251EOFRacesVoiceWatchdogWithoutDoubleAdvance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eof := make(chan struct{})
		p := recoveryPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
			if tr.Position == 1 {
				for range 50 {
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
				}
				select {
				case <-eof:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}, func(context.Context) (Voice, error) { return &fakeVoice{}, nil })
		if err := p.EnqueueMany([]media.Track{{Title: "current", Position: 1, Duration: 2 * time.Second}, {Title: "next", Position: 2}, {Title: "last", Position: 3}}, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		go close(eof)
		go p.SuspendVoice()
		synctest.Wait()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		s := p.State()
		if s.Current == nil || s.Current.Position != 2 || len(s.Queued) != 1 || s.Queued[0].Position != 3 {
			t.Fatal("watchdog/EOF duplicated or advanced twice", s)
		}
	})
}
