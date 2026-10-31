package player

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

func optPlayer(t *testing.T, g *media.ProviderAdmission) (*GuildPlayer, *fakeVoice) {
	t.Helper()
	p := New(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err := p.ConfigureYouTube(g, media.VerificationPrefetchHorizon); err != nil {
		t.Fatal(err)
	}
	v := &fakeVoice{}
	if err := p.Attach(v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close(context.Background()) })
	return p, v
}
func optTracks(n int, d time.Duration) []media.Track {
	tracks := numberedTracks(1, n)
	for i := range tracks {
		tracks[i].Duration = d
	}
	return tracks
}
func TestOPT026GlobalTwoGuildRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, err := media.NewProviderAdmission(media.ProviderAdmissionConfig{Concurrency: 4})
		if err != nil {
			t.Fatal(err)
		}
		p, v := optPlayer(t, gate)
		q, w := optPlayer(t, gate)
		var calls, notices atomic.Int32
		var blocked atomic.Bool
		blocked.Store(true)
		match := func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			release, _, err := gate.Admit(ctx)
			if err != nil {
				return media.Track{}, err
			}
			defer release()
			calls.Add(1)
			if blocked.Load() {
				failure := &media.ExtractionError{Category: "youtube_bot_challenge"}
				gate.Observe(failure)
				return media.Track{}, failure
			}
			return matched(tr), nil
		}
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		q.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		j := begin(t, p, context.Background(), optTracks(123, 210*time.Second), match)
		synctest.Wait()
		k := begin(t, q, context.Background(), optTracks(123, 210*time.Second), match)
		synctest.Wait()
		if calls.Load() != 1 || notices.Load() != 2 {
			t.Fatal(calls.Load(), notices.Load())
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 4 || gate.Snapshot().Attempts != 3 || !p.Health().RecoveryExhausted || !q.Health().RecoveryExhausted {
			t.Fatal(calls.Load(), gate.Snapshot(), p.Health(), q.Health())
		}
		for range 5 {
			if _, err := p.SetPaused(false); err == nil {
				t.Fatal("guild resume rearmed global budget")
			}
			if _, err := q.SetPaused(false); err == nil {
				t.Fatal("second guild reset budget")
			}
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != 4 || len(p.State().Entries) != 123 || len(q.State().Entries) != 123 || notices.Load() != 2 {
			t.Fatal("storm, queue loss or duplicate notice", calls.Load(), notices.Load())
		}
		v.mu.Lock()
		closed := v.closed
		v.mu.Unlock()
		w.mu.Lock()
		closed = closed || w.closed
		w.mu.Unlock()
		if closed {
			t.Fatal("unexpected Voice Leave")
		}
		blocked.Store(false)
		gate.Rearm()
		synctest.Wait()
		if p.State().Current == nil || q.State().Current == nil || p.State().Current.Position != 1 || q.State().Current.Position != 1 || len(p.State().Entries) != 122 || len(q.State().Entries) != 122 {
			t.Fatal("recovery changed independent queues")
		}
		if notices.Load() != 2 {
			t.Fatal("duplicate recovery notice")
		}
		t.Log("global recovery: initial=1 trials=3 guilds=2 positions_preserved=246 notices=2")
		p.Stop()
		q.Stop()
		synctest.Wait()
		jobDone(t, j)
		jobDone(t, k)
	})
}
func TestOPT026Horizon123AndDistantPlaylist(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _ := optPlayer(t, nil)
		var calls, far atomic.Int32
		j := begin(t, p, context.Background(), optTracks(123, 210*time.Second), func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
			calls.Add(1)
			return matched(tr), nil
		})
		synctest.Wait()
		if calls.Load() != 3 || len(p.State().Entries) != 122 {
			t.Fatal(calls.Load(), len(p.State().Entries))
		}
		k := begin(t, p, context.Background(), optTracks(123, 210*time.Second), func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
			far.Add(1)
			return matched(tr), nil
		})
		synctest.Wait()
		if far.Load() != 0 || len(p.State().Entries) != 245 {
			t.Fatal("distant collection resolved early", far.Load())
		}
		if first := firstResult(t, k); !first.Deferred {
			t.Fatal("command admission held until distant playback", first)
		}
		if _, err := p.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		p.confirmedOffset = 180 * time.Second
		p.wakeCollectionsLocked()
		p.mu.Unlock()
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("background work while paused")
		}
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 4 {
			t.Fatal("horizon didn't wake on resume", calls.Load())
		}
		result, err := p.SkipN(120)
		if err != nil || result.Skipped != 120 {
			t.Fatal(result, err)
		}
		synctest.Wait()
		state := p.State()
		if state.Current == nil || state.Current.Position != 121 || len(state.Entries) != 125 {
			t.Fatal("skip reordered/dropped positions", state.Current, len(state.Entries))
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
		jobDone(t, k)
		t.Log("prefetch before first EOF: first123=3 distant123=0; pause/resume/skip/stop preserved order")
	})
}
func TestOPT026HorizonShortLongUnknownAndDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration time.Duration
		want     int32
	}{{"short", 30 * time.Second, 7}, {"long", 20 * time.Minute, 1}, {"unknown", 0, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, _ := optPlayer(t, nil)
				var calls atomic.Int32
				tracks := optTracks(123, tc.duration)
				for i := range tracks {
					tracks[i].Title = "duplicate occurrence"
				}
				j := begin(t, p, context.Background(), tracks, func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
					calls.Add(1)
					return matched(tr), nil
				})
				synctest.Wait()
				if calls.Load() != tc.want || len(p.State().Entries) != 122 {
					t.Fatal(calls.Load(), tc.want, len(p.State().Entries))
				}
				for i, entry := range p.State().Entries {
					if entry.Track.Position != i+2 {
						t.Fatal("duplicate position lost", entry.Track.Position)
					}
				}
				if tc.name == "long" {
					p.mu.Lock()
					p.confirmedOffset = 13 * time.Minute
					p.wakeCollectionsLocked()
					p.mu.Unlock()
					synctest.Wait()
					if calls.Load() != 2 {
						t.Fatal("confirmed progress didn't unlock next track", calls.Load())
					}
				}
				p.Stop()
				synctest.Wait()
				jobDone(t, j)
			})
		})
	}
}
func TestOPT026HealthyAndLocalAudioIgnoreOtherGuildOutage(t *testing.T) {
	gate, err := media.NewProviderAdmission(media.ProviderAdmissionConfig{Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	p, v := optPlayer(t, gate)
	if err := p.Enqueue(media.Track{Title: "already healthy", URL: "fixture", Duration: time.Hour}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	waitLocal(t, func() bool { return p.State().CurrentStarted })
	gate.Observe(&media.ExtractionError{Category: "youtube_bot_challenge"})
	if p.State().Current == nil || p.State().Current.Title != "already healthy" {
		t.Fatal("healthy playback interrupted")
	}
	v.mu.Lock()
	closed := v.closed
	v.mu.Unlock()
	if closed {
		t.Fatal("Voice left on unrelated provider failure")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	file := wav(t)
	s := Streamer{Admission: gate, YTDLP: "/never-run", FFmpeg: ff, LocalAudioDir: filepath.Dir(file)}
	tr, err := media.LocalTrack(filepath.Base(file), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Play(context.Background(), tr, &recordingVoice{}); err != nil {
		t.Fatal("local audio blocked", err)
	}
}

func TestOPT026GlobalProgressRecoveryPolicy(t *testing.T) {
	for _, percent := range []int{10, 49, 50, 75, 99} {
		t.Run(strconv.Itoa(percent), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate, err := media.NewProviderAdmission(media.ProviderAdmissionConfig{Concurrency: 4})
				if err != nil {
					t.Fatal(err)
				}
				var calls, notices atomic.Int32
				var selected []media.Track
				p := New(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), func(ctx context.Context, tr media.Track, v Voice) error {
					release, _, err := gate.Admit(ctx)
					if err != nil {
						return err
					}
					defer release()
					selected = append(selected, tr)
					if calls.Add(1) == 1 {
						for range percent * 5 {
							if err := v.WriteFrame(ctx, []byte{1}); err != nil {
								return err
							}
						}
						failure := &media.ExtractionError{Category: "youtube_bot_challenge"}
						gate.Observe(failure)
						return failure
					}
					for range 250 {
						if err := v.WriteFrame(ctx, []byte{1}); err != nil {
							return err
						}
					}
					<-ctx.Done()
					return ctx.Err()
				})
				defer p.Close(context.Background())
				if err := p.ConfigureYouTube(gate, media.VerificationPrefetchHorizon); err != nil {
					t.Fatal(err)
				}
				voice := &fakeVoice{}
				if err := p.Attach(voice); err != nil {
					t.Fatal(err)
				}
				p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
				if err := p.EnqueueMany(optTracks(3, 10*time.Second), p.Ticket()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				wantPosition := 1
				if percent >= 50 {
					wantPosition = 2
				}
				state := p.State()
				if !state.ProviderBlocked || state.Entries[0].Track.Position != wantPosition {
					t.Fatal(state)
				}
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if calls.Load() != 2 || selected[1].Position != wantPosition || notices.Load() != 1 {
					t.Fatal(calls.Load(), selected, notices.Load())
				}
				if percent < 50 && selected[1].ResumeOffset != time.Duration(percent)*100*time.Millisecond {
					t.Fatal("checkpoint lost", selected[1].ResumeOffset)
				}
				voice.mu.Lock()
				closed := voice.closed
				voice.mu.Unlock()
				if closed {
					t.Fatal("Voice Leave")
				}
			})
		})
	}
}

func TestOPT026PauseDuringGlobalBackoffRechecksHorizon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, err := media.NewProviderAdmission(media.ProviderAdmissionConfig{Concurrency: 4})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := optPlayer(t, gate)
		if err := p.Enqueue(matched(optTracks(1, 30*time.Second)[0]), p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		var calls atomic.Int32
		j := begin(t, p, context.Background(), optTracks(123, 210*time.Second), func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
			release, _, err := gate.Admit(ctx)
			if err != nil {
				return media.Track{}, err
			}
			defer release()
			calls.Add(1)
			failure := &media.ExtractionError{Category: "youtube_bot_challenge"}
			gate.Observe(failure)
			return media.Track{}, failure
		})
		synctest.Wait()
		if _, err := p.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 || gate.Snapshot().Attempts != 0 {
			t.Fatal("paused recovery started extraction or consumed trial", calls.Load(), gate.Snapshot())
		}
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if p.pause.paused() {
			t.Fatal("global outage prevented healthy audio resume")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 4 || !gate.Snapshot().Exhausted {
			t.Fatal(calls.Load(), gate.Snapshot())
		}
		if _, err := p.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal("exhausted provider prevents healthy stream resume", err)
		}
		if gate.Snapshot().Attempts != 3 {
			t.Fatal("healthy resume reset global budget")
		}
		p.Stop()
		synctest.Wait()
		jobDone(t, j)
	})
}
