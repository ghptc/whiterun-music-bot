package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
)

func TestFIX025ConfirmedProgressPolicy123Occurrences(t *testing.T) {
	for _, component := range []string{"voice", "media"} {
		for _, percent := range []int{10, 25, 49, 50, 75, 99} {
			t.Run(fmt.Sprintf("%s/%d", component, percent), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var calls, notices atomic.Int32
					var selected []media.Track
					p := recoveryPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
						selected = append(selected, tr)
						if calls.Add(1) == 1 {
							for range percent {
								if err := v.WriteFrame(ctx, []byte{1}); err != nil {
									return err
								}
							}
							if component == "media" {
								return &StreamError{"audio_stall", errors.New("injected audio source stall")}
							}
							return &StreamError{"voice_send", errors.New("injected UDP failure")}
						}
						if err := v.WriteFrame(ctx, []byte{1}); err != nil {
							return err
						}
						<-ctx.Done()
						return ctx.Err()
					}, func(context.Context) (Voice, error) { return &fakeVoice{}, nil })
					p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
					tracks := make([]media.Track, 123)
					for i := range tracks {
						tracks[i] = media.Track{Title: "legitimate duplicate", VideoID: "abcdefghijk", Position: i + 1, Total: 123, Duration: 2 * time.Second}
					}
					if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					time.Sleep(3 * time.Second)
					synctest.Wait()
					wantPosition, wantQueue := 1, 122
					if percent >= 50 {
						wantPosition, wantQueue = 2, 121
					}
					state := p.State()
					wantNotices := int32(1)
					if component == "media" {
						wantNotices = 0
					}
					if calls.Load() != 2 || notices.Load() != wantNotices || state.Current == nil || state.Current.Position != wantPosition || len(state.Queued) != wantQueue {
						t.Fatalf("calls=%d notices=%d state=%+v", calls.Load(), notices.Load(), state)
					}
					if percent < 50 && selected[1].ResumeOffset != time.Duration(percent)*20*time.Millisecond {
						t.Fatal("lost confirmed progress", selected)
					}
					for i, tr := range state.Queued {
						if tr.Position != wantPosition+i+1 {
							t.Fatal("order changed", tr)
						}
					}
				})
			})
		}
	}
}

func TestFIX025CrashCheckpointRequiresAttachment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &qualityStore{}
		p := qualityDetached(t)
		if err := p.ConfigureStore(store, "guild"); err != nil {
			t.Fatal(err)
		}
		p.play = func(ctx context.Context, _ media.Track, v Voice) error {
			for range 600 {
				if err := v.WriteFrame(ctx, []byte{1}); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return ctx.Err()
		}
		if err := p.Attach(&fakeVoice{}); err != nil {
			t.Fatal(err)
		}
		if err := p.EnqueueMany([]media.Track{{Title: "current", Duration: 100 * time.Second}, {Title: "next"}}, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		saved := store.state
		if saved.Current == nil || saved.Current.ResumeOffset != 10*time.Second {
			t.Fatalf("checkpoint: %+v", saved.Current)
		}
		restored := qualityDetached(t)
		if err := restored.ConfigureStore(&qualityStore{state: saved}, "guild"); err != nil {
			t.Fatal(err)
		}
		state := restored.State()
		if state.Current != nil || len(state.Queued) != 2 || state.Queued[0].ResumeOffset != 10*time.Second {
			t.Fatal(state)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if restored.State().Current != nil {
			t.Fatal("unauthorized automatic playback")
		}
	})
}

func TestFIX025FailedResumePreservesPosition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		p := recoveryPlayer(t, func(context.Context, media.Track, Voice) error {
			calls.Add(1)
			return &StreamError{"ffmpeg_exit", errors.New("seek failed")}
		}, func(context.Context) (Voice, error) { return &fakeVoice{}, nil })
		if err := p.EnqueueMany([]media.Track{{Title: "saved", ResumeOffset: 10 * time.Second, Duration: 100 * time.Second}, {Title: "next"}}, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		state := p.State()
		if calls.Load() != 1 || !state.Halted || len(state.Queued) != 2 || state.Queued[0].ResumeOffset != 10*time.Second {
			t.Fatal(state)
		}
	})
}

func TestFIX025PauseDuringRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		p := recoveryPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
			if calls.Add(1) == 1 {
				return &StreamError{"voice_send", errors.New("closed")}
			}
			if _, err := waitPlayback(ctx); err != nil {
				return err
			}
			_ = v.WriteFrame(ctx, []byte{1})
			<-ctx.Done()
			return ctx.Err()
		}, func(context.Context) (Voice, error) { return &fakeVoice{}, nil })
		enqueueRecovery(t, p, 3)
		synctest.Wait()
		if _, err := p.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if !p.State().Paused || p.State().CurrentStarted {
			t.Fatal("pause overridden")
		}
		if _, err := p.SetPaused(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !p.State().CurrentStarted {
			t.Fatal("resume failed")
		}
	})
}

func TestFIX025AccurateSeekWithRealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	file := wav(t)
	yt := executable(t, "yt-dlp", fmt.Sprintf("cat '%s'\n", file))
	stream := Streamer{YTDLP: yt, FFmpeg: ff}
	full, seek := &recordingVoice{}, &recordingVoice{}
	if err = stream.Play(context.Background(), media.Track{URL: "https://youtu.be/abcdefghijk"}, full); err != nil {
		t.Fatal(err)
	}
	if err = stream.Play(context.Background(), media.Track{URL: "https://youtu.be/abcdefghijk", ResumeOffset: 40 * time.Millisecond}, seek); err != nil {
		t.Fatal(err)
	}
	if seek.frames <= 5 || seek.frames >= full.frames {
		t.Fatalf("seek didn't discard confirmed audio: full=%d resumed=%d", full.frames, seek.frames)
	}
}

func TestFIX025SingleFrameCannotResetIncidentBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, notices atomic.Int32
		var v *recoveryVoice
		v = &recoveryVoice{recover: func(context.Context) (Voice, error) { return v, nil }}
		p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v Voice) error {
			calls.Add(1)
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			return &StreamError{"voice_send", errors.New("failed after one packet")}
		})
		defer p.Close(context.Background())
		if err := p.Attach(v); err != nil {
			t.Fatal(err)
		}
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		enqueueRecovery(t, p, 123)
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != 6 || notices.Load() != 1 || p.Length() != 123 || !p.State().VoiceSuspended || len(p.History()) != 1 {
			t.Fatalf("calls=%d notices=%d history=%d", calls.Load(), notices.Load(), len(p.History()))
		}
	})
}

type busyCheckpointStore struct {
	busy     atomic.Bool
	periodic atomic.Int32
}

func (*busyCheckpointStore) LoadGuild(context.Context, string) (DurableState, error) {
	return DurableState{}, nil
}
func (s *busyCheckpointStore) SaveGuild(ctx context.Context, _ string, state DurableState) error {
	if s.busy.Load() && state.Current != nil && state.Current.ResumeOffset >= 5*time.Second {
		s.periodic.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func TestFIX025BusyCheckpointCannotBlockAudioForCommandTimeout(t *testing.T) {
	store := &busyCheckpointStore{}
	store.busy.Store(true)
	sent := make(chan struct{})
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v Voice) error {
		for range 600 {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
		}
		close(sent)
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.Close(context.Background())
	defer store.busy.Store(false)
	if err := p.ConfigureStore(store, "busy"); err != nil {
		t.Fatal(err)
	}
	if err := p.Attach(&fakeVoice{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(media.Track{Title: "test", Duration: 100 * time.Second}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("periodic checkpoint blocked audio for the 3s command deadline")
	}
	if store.periodic.Load() != 2 || p.State().PersistenceError == "" || !p.Health().Responsive {
		t.Fatal("failed checkpoint was hidden or blocked player")
	}
}
