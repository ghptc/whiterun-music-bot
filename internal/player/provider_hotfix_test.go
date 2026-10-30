package player

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

func TestProviderBackoffPreserves123PositionsAndRecoversWithoutLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var blocked atomic.Bool
		blocked.Store(true)
		var calls, notices, starts atomic.Int32
		p, v := newTestPlayer(t, func(ctx context.Context, tr media.Track, voice Voice) error {
			starts.Add(1)
			if err := voice.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		defer p.Close(context.Background())
		p.SetOnSkip(func(e PlaybackEvent) {
			if e.FailureStage != "provider" || e.State != Suspended {
				t.Error("track marked unavailable", e)
			}
			notices.Add(1)
		})
		j := begin(t, p, context.Background(), numberedTracks(1, 123), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			calls.Add(1)
			if blocked.Load() {
				return media.Track{}, &media.ExtractionError{Category: "youtube_bot_challenge"}
			}
			return matched(tr), nil
		})
		synctest.Wait()
		if !firstResult(t, j).Suspended {
			t.Fatal("initial command left waiting")
		}
		if calls.Load() != 1 || len(p.State().Entries) != 123 || !p.State().ProviderBlocked || notices.Load() != 1 {
			t.Fatalf("initial block: calls=%d state=%+v notices=%d", calls.Load(), p.State(), notices.Load())
		}
		time.Sleep(14 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("backoff bypassed")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("first trial missing", calls.Load())
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("second trial missing", calls.Load())
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 4 {
			t.Fatal("third trial missing", calls.Load())
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != 4 || notices.Load() != 1 || j.progress.Failed != 0 || len(p.State().Entries) != 123 || !p.Health().RecoveryExhausted {
			t.Fatal("budget/spam/preservation failure", calls.Load(), notices.Load(), j.progress)
		}
		blocked.Store(false)
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if p.State().ProviderBlocked || starts.Load() != 1 || p.State().Current == nil || p.State().Current.Position != 1 {
			t.Fatal("did not resume same position", p.State())
		}
		if j.progress.Failed != 0 || notices.Load() != 1 {
			t.Fatal("duplicate failure notification")
		}
		v.mu.Lock()
		closed := v.closed
		v.mu.Unlock()
		if closed {
			t.Fatal("unexpected Voice Leave")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
	})
}

func TestProviderPlaybackConfirmedProgressPolicy(t *testing.T) {
	for _, frames := range []int{10, 49, 50, 75, 99} {
		t.Run(fmt.Sprint(frames), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls, notices atomic.Int32
				var selected []media.Track
				p, v := newTestPlayer(t, func(ctx context.Context, tr media.Track, voice Voice) error {
					selected = append(selected, tr)
					if calls.Add(1) == 1 {
						for range frames * 5 {
							if err := voice.WriteFrame(ctx, []byte{1}); err != nil {
								return err
							}
						}
						return &media.ExtractionError{Category: "youtube_auth_required"}
					}
					for range 250 {
						if err := voice.WriteFrame(ctx, []byte{1}); err != nil {
							return err
						}
					}
					<-ctx.Done()
					return ctx.Err()
				})
				defer p.Close(context.Background())
				p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
				tracks := numberedTracks(1, 3)
				for i := range tracks {
					tracks[i].Duration = 10 * time.Second
				}
				if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				wantPosition, wantEntries := 1, 3
				if frames >= 50 {
					wantPosition, wantEntries = 2, 2
				}
				state := p.State()
				if !state.ProviderBlocked || len(state.Entries) != wantEntries || state.Entries[0].Track.Position != wantPosition || notices.Load() != 1 {
					t.Fatalf("policy lost before recovery: %+v", state)
				}
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if calls.Load() != 2 || selected[1].Position != wantPosition || p.State().ProviderBlocked || notices.Load() != 1 {
					t.Fatal("duplicate advancement or recovery failure", selected)
				}
				if frames < 50 && selected[1].ResumeOffset != time.Duration(frames)*100*time.Millisecond {
					t.Fatal("checkpoint lost", selected[1].ResumeOffset)
				}
				v.mu.Lock()
				closed := v.closed
				v.mu.Unlock()
				if closed {
					t.Fatal("unexpected Leave")
				}
			})
		})
	}
}

func TestProviderTrialAdmissionSerializesConcurrentCollections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		p.mu.Lock()
		p.blockProviderLocked("youtube_bot_challenge")
		p.mu.Unlock()
		var calls, active, maximum atomic.Int32
		match := func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			calls.Add(1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old; old = maximum.Load() {
				if maximum.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Second)
			return media.Track{}, &media.ExtractionError{Category: "youtube_bot_challenge"}
		}
		for range 3 {
			begin(t, p, context.Background(), numberedTracks(1, 10), match)
		}
		time.Sleep(15 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 || maximum.Load() != 1 {
			t.Fatal("parallel recovery probes", calls.Load(), maximum.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(p.State().Entries) != 30 || calls.Load() != 1 {
			t.Fatal("queue consumed")
		}
		p.Stop()
		synctest.Wait()
	})
}

func TestProviderBackgroundFailureKeepsCurrentAudioAndPendingQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		playing := make(chan struct{})
		var notices, calls atomic.Int32
		p, v := newTestPlayer(t, func(ctx context.Context, tr media.Track, voice Voice) error {
			if err := voice.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			close(playing)
			<-ctx.Done()
			return ctx.Err()
		})
		defer p.Close(context.Background())
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		j := begin(t, p, context.Background(), numberedTracks(1, 8), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			calls.Add(1)
			if tr.Position == 1 {
				return matched(tr), nil
			}
			select {
			case <-playing:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
			if tr.Position == 2 {
				return media.Track{}, &media.ExtractionError{Category: "youtube_rate_limited"}
			}
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		})
		synctest.Wait()
		state := p.State()
		if !state.ProviderBlocked || !state.CurrentStarted || state.Current == nil || state.Current.Position != 1 || len(state.Entries) != 7 || j.progress.Failed != 0 || notices.Load() != 1 {
			t.Fatal("background failure damaged active playback", state, j.progress)
		}
		before := calls.Load()
		time.Sleep(14 * time.Second)
		synctest.Wait()
		if calls.Load() != before {
			t.Fatal("retry cascade during cooldown")
		}
		v.mu.Lock()
		closed := v.closed
		v.mu.Unlock()
		if closed {
			t.Fatal("background failure closed Voice")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
	})
}

func TestProviderPreparationFailureCanResumeAnEmptyQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		var notices atomic.Int32
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		err := &media.ExtractionError{Category: "youtube_auth_required"}
		p.SuspendProvider(err)
		p.SuspendProvider(err)
		if !p.State().ProviderBlocked || notices.Load() != 1 {
			t.Fatal("preparation incident duplicated or lost")
		}
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal(err)
		}
		if p.State().ProviderBlocked {
			t.Fatal("empty queue cannot accept a new request after resume")
		}
		add(t, p, "fresh request")
		synctest.Wait()
		if p.State().Current == nil || p.State().Current.Title != "fresh request" {
			t.Fatal("fresh request stalled")
		}
	})
}

func TestProviderOneFrameDoesNotResetRetryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, notices atomic.Int32
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
			calls.Add(1)
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			return &media.ExtractionError{Category: "youtube_bot_challenge"}
		})
		defer p.Close(context.Background())
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		if err := p.EnqueueMany([]media.Track{{Title: "current", Position: 1, Duration: 100 * time.Second}, {Title: "next", Position: 2}}, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()
		state := p.State()
		if calls.Load() != 4 || notices.Load() != 1 || !state.ProviderBlocked || len(state.Entries) != 2 || state.Entries[0].Track.ResumeOffset != 80*time.Millisecond || len(p.History()) != 1 {
			t.Fatal("one frame reset incident budget or duplicated history", calls.Load(), notices.Load(), state)
		}
	})
}

func TestProviderFailurePersistsSavedCheckpointWithoutConsumingQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &qualityStore{state: DurableState{Current: &media.Track{Title: "saved", Position: 1, ResumeOffset: 20 * time.Second, Duration: 100 * time.Second}, Queue: []media.Track{{Title: "next", Position: 2}}}}
		p := qualityDetached(t)
		defer p.Close(context.Background())
		if err := p.ConfigureStore(store, "guild"); err != nil {
			t.Fatal(err)
		}
		p.play = func(context.Context, media.Track, Voice) error {
			return &media.ExtractionError{Category: "youtube_bot_challenge"}
		}
		if err := p.Attach(&fakeVoice{}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if store.state.Current != nil || len(store.state.Queue) != 2 || store.state.Queue[0].ResumeOffset != 20*time.Second || store.state.Queue[1].Title != "next" {
			t.Fatal("durable queue/checkpoint consumed", store.state)
		}
		restored := qualityDetached(t)
		defer restored.Close(context.Background())
		if err := restored.ConfigureStore(&qualityStore{state: store.state}, "guild"); err != nil {
			t.Fatal(err)
		}
		if restored.State().Current != nil || len(restored.State().Entries) != 2 {
			t.Fatal("restore played without attachment or lost queue")
		}
	})
}

func TestProviderStopCancelsActiveTrialAndAllowsFreshCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		p, v := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		j := begin(t, p, context.Background(), numberedTracks(1, 3), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			if calls.Add(1) == 1 {
				return media.Track{}, &media.ExtractionError{Category: "youtube_bot_challenge"}
			}
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		})
		synctest.Wait()
		time.Sleep(15 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("trial did not start")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
		if p.State().ProviderBlocked || p.Length() != 0 {
			t.Fatal("canceled trial resurrected provider block")
		}
		fresh := begin(t, p, context.Background(), numberedTracks(1, 1), func(_ context.Context, tr media.Track, _ bool) (media.Track, error) { return matched(tr), nil })
		synctest.Wait()
		jobDone(t, fresh)
		if p.State().Current == nil {
			t.Fatal("fresh collection blocked by old trial")
		}
		v.mu.Lock()
		closed := v.closed
		v.mu.Unlock()
		if closed {
			t.Fatal("Stop recovery unexpectedly left Voice")
		}
	})
}

func TestProviderStreamCategoriesUseTheSameRecoveryCircuit(t *testing.T) {
	for _, diagnostic := range []string{"Sign in to confirm you're not a bot", "Authentication required", "HTTP Error 429: Too Many Requests", "Connection timed out"} {
		category := sourceFailure([]byte(diagnostic))
		if !isProviderFailure(&StreamError{Category: category, Err: fmt.Errorf("controlled producer failure")}) {
			t.Fatal("producer category would consume queue instead of suspending", category)
		}
	}
	if isProviderFailure(&StreamError{Category: sourceFailure([]byte("Video unavailable")), Err: fmt.Errorf("controlled video failure")}) {
		t.Fatal("single unavailable video blocked provider")
	}
}

func TestProviderLateCanceledResolutionCannotConsumePositionAfterRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		playing := make(chan struct{})
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error {
			close(playing)
			<-ctx.Done()
			return ctx.Err()
		})
		defer p.Close(context.Background())
		var challenged, delayed atomic.Bool
		j := begin(t, p, context.Background(), numberedTracks(1, 8), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			if tr.Position == 1 {
				return matched(tr), nil
			}
			if tr.Position == 2 && !challenged.Swap(true) {
				<-playing
				// Let another resolver register before triggering its cancellation.
				time.Sleep(time.Second)
				return media.Track{}, &media.ExtractionError{Category: "youtube_bot_challenge"}
			}
			if tr.Position == 3 && !delayed.Swap(true) {
				<-ctx.Done()
				time.Sleep(20 * time.Second)
				return media.Track{}, ctx.Err()
			}
			return matched(tr), nil
		})
		synctest.Wait()
		time.Sleep(25 * time.Second)
		synctest.Wait()
		if !delayed.Load() || j.progress.Failed != 0 || len(p.State().Entries) != 7 || p.State().ProviderBlocked {
			t.Fatal("late canceled resolver consumed a position", p.State(), j.progress)
		}
		found := false
		for _, entry := range p.State().Entries {
			if entry.Track.Position == 3 && !entry.Pending {
				found = true
			}
		}
		if !found {
			t.Fatal("canceled position was not retried after provider recovered")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
	})
}

func TestProviderCollectionRepliesWhileRecoveryBudgetIsExhausted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		p.mu.Lock()
		p.blockProviderLocked("youtube_bot_challenge")
		p.providerRetries = providerAutomaticRetries
		p.mu.Unlock()
		var calls atomic.Int32
		j := begin(t, p, context.Background(), numberedTracks(1, 2), func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
			calls.Add(1)
			return matched(tr), nil
		})
		synctest.Wait()
		if !firstResult(t, j).Suspended || calls.Load() != 0 || len(p.State().Entries) != 2 {
			t.Fatal("queued command hung or bypassed exhausted budget")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
	})
}
