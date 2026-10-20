package player

import (
	"context"
	"testing"
	"testing/synctest"
	"whiterun/internal/media"
)

func TestQA013StartingControlNeverAnnouncesCanceledAttempt(t *testing.T) {
	for _, action := range []string{"skip", "stop", "leave", "replay"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				frame := make(chan struct{})
				p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
					select {
					case <-frame:
					case <-ctx.Done():
						return ctx.Err()
					}
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				})
				if err := p.EnqueueMany(numberedTracks(1, 2), p.Ticket()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				s := p.State()
				if s.PlaybackState != Starting || s.CurrentStarted || len(p.History()) != 0 {
					t.Fatal("fixture did not enter real STARTING state")
				}
				switch action {
				case "skip":
					p.Skip()
				case "stop":
					p.Stop()
				case "leave":
					p.Leave(context.Background())
				case "replay":
					if err := p.Replay(); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				if len(p.History()) != 0 {
					t.Fatal("canceled starting attempt entered history")
				}
				close(frame)
				synctest.Wait()
				s = p.State()
				switch action {
				case "skip":
					if s.Current == nil || s.Current.Position != 2 || len(p.History()) != 1 {
						t.Fatal("skip failed to start successor", s)
					}
				case "replay":
					if s.Current == nil || s.Current.Position != 1 || len(p.History()) != 1 {
						t.Fatal("replay did not start replacement attempt", s)
					}
				default:
					if s.Current != nil || p.Length() != 0 || len(p.History()) != 0 {
						t.Fatal("stop/leave resurrected canceled STARTING attempt")
					}
				}
				p.Stop()
				synctest.Wait()
			})
		})
	}
}
func TestQA013ProgressiveMutationSequences(t *testing.T) {
	for _, actions := range [][]string{{"shuffle", "move", "skip"}, {"shuffle", "remove", "EOF"}, {"shuffle", "jump"}, {"stop"}, {"clear", "EOF"}, {"leave"}} {
		name := ""
		for _, a := range actions {
			name += a + "_"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resolve, finish := make(chan struct{}), make(chan struct{})
				p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
					select {
					case <-finish:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				j := begin(t, p, context.Background(), numberedTracks(1, 20), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
					if tr.Position > 1 {
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
				expected := []int{}
				for i := 2; i <= 20; i++ {
					expected = append(expected, i)
				}
				ended := false
				for _, action := range actions {
					switch action {
					case "shuffle":
						if err := p.shuffle(func(n int, swap func(int, int)) {
							for i := 0; i < n/2; i++ {
								swap(i, n-1-i)
							}
						}); err != nil {
							t.Fatal(err)
						}
						for i, j := 0, len(expected)-1; i < j; i, j = i+1, j-1 {
							expected[i], expected[j] = expected[j], expected[i]
						}
					case "move":
						if err := p.Move(3, 1); err != nil {
							t.Fatal(err)
						}
						expected = append([]int{expected[2], expected[0], expected[1]}, expected[3:]...)
					case "remove":
						if err := p.Remove(2); err != nil {
							t.Fatal(err)
						}
						expected = append(expected[:1], expected[2:]...)
					case "skip":
						p.Skip()
						ended = true
					case "jump":
						if err := p.Jump(3); err != nil {
							t.Fatal(err)
						}
						expected = expected[2:]
						ended = true
					case "EOF":
						finish <- struct{}{}
						ended = true
					case "clear":
						p.Clear()
						expected = nil
					case "stop":
						p.Stop()
						expected = nil
						ended = true
					case "leave":
						p.Leave(context.Background())
						expected = nil
						ended = true
					}
					synctest.Wait()
				}
				close(resolve)
				synctest.Wait()
				state := p.State()
				if ended {
					if len(expected) > 0 {
						if state.Current == nil || state.Current.Position != expected[0] {
							t.Fatal("wrong successor after progressive mutations", state.Current, expected)
						}
						expected = expected[1:]
					} else if state.Current != nil {
						t.Fatal("canceled collection resurrected playback")
					}
				}
				if len(state.Entries) != len(expected) {
					t.Fatal("progressive queue length", len(state.Entries), len(expected))
				}
				for i, e := range state.Entries {
					if e.Track.Position != expected[i] || e.Position != expected[i] || e.CollectionID != j.progress.ID {
						t.Fatal("progressive slot identity changed", i, e)
					}
				}
				p.Stop()
				synctest.Wait()
				jobDone(t, j)
			})
		})
	}
}
