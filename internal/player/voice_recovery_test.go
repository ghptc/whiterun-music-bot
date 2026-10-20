package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

type recoveryVoice struct {
	fakeVoice
	recover func(context.Context) (Voice, error)
}

func (v *recoveryVoice) Recover(ctx context.Context) (Voice, error) { return v.recover(ctx) }

func recoveryPlayer(t *testing.T, play PlayFunc, recover func(context.Context) (Voice, error)) *GuildPlayer {
	t.Helper()
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), play)
	if err := p.Attach(&recoveryVoice{recover: recover}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close(context.Background()) })
	return p
}
func enqueueRecovery(t *testing.T, p *GuildPlayer, n int) {
	t.Helper()
	tracks := make([]media.Track, n)
	for i := range tracks {
		tracks[i] = media.Track{Title: fmt.Sprint(i), SpotifyID: fmt.Sprintf("spotify-%d", i), VideoID: fmt.Sprintf("verified-%d", i), RequestedSource: "spotify", Position: i + 1, Total: n}
	}
	if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
		t.Fatal(err)
	}
}
func retainedRecovery(t *testing.T, p *GuildPlayer, n int) {
	t.Helper()
	state := p.State()
	if !state.VoiceSuspended || state.Current != nil || len(state.Queued) != n {
		t.Fatalf("queue not retained: %+v", state)
	}
	for i, track := range state.Queued {
		if track.Position != i+1 || track.SpotifyID != fmt.Sprintf("spotify-%d", i) || track.VideoID != fmt.Sprintf("verified-%d", i) {
			t.Fatalf("fidelity/order changed: %+v", track)
		}
	}
}

func TestVoiceFailureClassificationJoinedCleanup(t *testing.T) {
	for _, category := range []string{"voice_send", "voice_speaking", "voice_transport"} {
		err := errors.Join(&StreamError{"ffmpeg_exit", errors.New("cleanup")}, fmt.Errorf("wrapped: %w", &StreamError{category, errors.New("shard is not ready")}))
		if failureCategory(err) != "voice_transport" || !IsVoiceFailure(err) {
			t.Fatal(err)
		}
	}
	for _, category := range []string{"no_audio", "ffmpeg_exit", "youtube_auth_challenge", "audio_stall"} {
		err := &StreamError{category, errors.New("media")}
		if IsVoiceFailure(err) || failureCategory(err) != category {
			t.Fatal(err)
		}
	}
}

func TestVoiceUnavailableBeforeAndDuringPlaybackRecoversAfterFiveSeconds(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				available := time.Now().Add(5 * time.Second)
				var calls, reconnects, notices atomic.Int32
				var selected []string
				p := recoveryPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
					calls.Add(1)
					selected = append(selected, track.SpotifyID)
					if calls.Load() == 1 {
						if started {
							if err := v.WriteFrame(ctx, []byte{1}); err != nil {
								return err
							}
						}
						return &StreamError{"voice_speaking", errors.New("shard is not ready")}
					}
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				}, func(ctx context.Context) (Voice, error) {
					reconnects.Add(1)
					if time.Now().Before(available) {
						return nil, errors.New("offline")
					}
					return &fakeVoice{}, nil
				})
				p.SetOnSkip(func(e PlaybackEvent) {
					if e.FailureStage != "voice" {
						t.Error("transport marked unavailable")
					}
					notices.Add(1)
				})
				enqueueRecovery(t, p, 300)
				synctest.Wait()
				retainedRecovery(t, p, 300)
				time.Sleep(5 * time.Second)
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatal("streaming attempted while offline", calls.Load())
				}
				time.Sleep(5 * time.Second)
				synctest.Wait()
				if calls.Load() != 2 || reconnects.Load() != 3 || notices.Load() != 1 || !reflect.DeepEqual(selected, []string{"spotify-0", "spotify-0"}) {
					t.Fatalf("calls=%d reconnects=%d notices=%d selected=%v", calls.Load(), reconnects.Load(), notices.Load(), selected)
				}
				state := p.State()
				if state.Current == nil || state.Current.SpotifyID != "spotify-0" || len(state.Queued) != 299 || state.VoiceSuspended {
					t.Fatal("recovery lost queue")
				}
			})
		})
	}
}

func TestVoicePersistentFailurePreserves300TracksAndBoundsAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, reconnects, notices atomic.Int32
		p := recoveryPlayer(t, func(context.Context, media.Track, Voice) error {
			calls.Add(1)
			return &StreamError{"voice_send", errors.New("closed UDP")}
		}, func(context.Context) (Voice, error) { reconnects.Add(1); return nil, errors.New("offline") })
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		enqueueRecovery(t, p, 300)
		synctest.Wait()
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		retainedRecovery(t, p, 300)
		// Repeated health reports and enqueue cannot reset an exhausted incident.
		for range 10 {
			p.SuspendVoice()
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != 1 || reconnects.Load() != 5 || notices.Load() != 1 {
			t.Fatalf("cascade: play=%d reconnect=%d notices=%d", calls.Load(), reconnects.Load(), notices.Load())
		}
	})
}

func TestVoiceStopAndSkipDuringRecovery(t *testing.T) {
	for _, command := range []string{"stop", "skip"} {
		t.Run(command, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls, reconnects atomic.Int32
				var tracks []string
				p := recoveryPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
					calls.Add(1)
					tracks = append(tracks, track.Title)
					if calls.Load() == 1 {
						return &StreamError{"voice_send", errors.New("closed")}
					}
					_ = v.WriteFrame(ctx, []byte{1})
					<-ctx.Done()
					return ctx.Err()
				}, func(ctx context.Context) (Voice, error) {
					reconnects.Add(1)
					timer := time.NewTimer(5 * time.Second)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-timer.C:
						return &fakeVoice{}, nil
					}
				})
				enqueueRecovery(t, p, 3)
				synctest.Wait()
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if command == "stop" {
					p.Stop()
				} else if !p.Skip() {
					t.Fatal("skip failed")
				}
				synctest.Wait()
				time.Sleep(time.Minute)
				synctest.Wait()
				state := p.State()
				if command == "stop" {
					if calls.Load() != 1 || state.Current != nil || len(state.Queued) != 0 {
						t.Fatal("stop resurrected playback")
					}
				} else {
					if calls.Load() != 2 || reconnects.Load() != 1 || !reflect.DeepEqual(tracks, []string{"0", "1"}) || len(state.Queued) != 1 {
						t.Fatalf("skip during suspension: calls=%d tracks=%v state=%+v", calls.Load(), tracks, state)
					}
				}
			})
		})
	}
}

func TestVoiceHealthLossCancelsActiveAudioAndGuildsAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, cleanups atomic.Int32
		play := func(ctx context.Context, _ media.Track, v Voice) error {
			calls.Add(1)
			defer func() { cleanups.Add(1) }()
			_ = v.WriteFrame(ctx, []byte{1})
			<-ctx.Done()
			return ctx.Err()
		}
		offline := func(context.Context) (Voice, error) { return nil, errors.New("offline") }
		first := recoveryPlayer(t, play, offline)
		other := recoveryPlayer(t, play, offline)
		enqueueRecovery(t, first, 3)
		enqueueRecovery(t, other, 3)
		synctest.Wait()
		first.SuspendVoice()
		synctest.Wait()
		retainedRecovery(t, first, 3)
		if cleanups.Load() != 1 || calls.Load() != 2 || other.State().VoiceSuspended || other.State().Current == nil {
			t.Fatal("loss did not clean up/isolate guild")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		retainedRecovery(t, first, 3)
		if other.State().Current == nil || len(other.State().Queued) != 2 {
			t.Fatal("other guild affected")
		}
	})
}

func TestVoiceDisconnectDuringTransitionAndStopDuringFailure(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				selected := 0
				release := make(chan struct{})
				p := recoveryPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error {
					selected++
					<-release
					return &StreamError{"voice_speaking", errors.New("disconnected during transition")}
				}, func(context.Context) (Voice, error) { return nil, errors.New("offline") })
				enqueueRecovery(t, p, 3)
				synctest.Wait()
				if stop {
					p.Stop()
				} else {
					p.SuspendVoice()
				}
				close(release)
				synctest.Wait()
				time.Sleep(time.Minute)
				synctest.Wait()
				if stop {
					if p.Length() != 0 || p.State().Current != nil {
						t.Fatal("canceled selection restored")
					}
				} else {
					retainedRecovery(t, p, 3)
				}
				if selected != 1 {
					t.Fatal("transition consumed queue")
				}
			})
		})
	}
}

func TestVoiceFalseRecoveryCannotResetBudgetOrSpam(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, notices atomic.Int32
		var v *recoveryVoice
		v = &recoveryVoice{recover: func(context.Context) (Voice, error) { return v, nil }}
		p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, media.Track, Voice) error {
			calls.Add(1)
			return &StreamError{"voice_send", errors.New("UDP unavailable")}
		})
		defer p.Close(context.Background())
		_ = p.Attach(v)
		p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
		enqueueRecovery(t, p, 300)
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()
		retainedRecovery(t, p, 300)
		if calls.Load() != 6 || notices.Load() != 1 {
			t.Fatalf("false readiness caused loop/spam: calls=%d notices=%d", calls.Load(), notices.Load())
		}
	})
}

type brokenStreamVoice struct {
	fakeVoice
	failBeforeFrame bool
	frames          int
}

func (v *brokenStreamVoice) Speaking(_ context.Context, on bool) error {
	if on && v.failBeforeFrame {
		return errors.New("shard is not ready")
	}
	return nil
}
func (v *brokenStreamVoice) WriteFrame(context.Context, []byte) error {
	v.frames++
	if v.frames >= 3 {
		return errors.New("UDP closed")
	}
	return nil
}

func TestVoiceStreamFailureReapsRealAudioProcesses(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			dir := t.TempDir()
			ytPID, ffPID := filepath.Join(dir, "yt.pid"), filepath.Join(dir, "ff.pid")
			// Keep the producer alive after output so cleanup has real owned processes
			// to terminate and reap on the transport error.
			audio := filepath.Join(dir, "long.wav")
			data, err := os.ReadFile(wav(t))
			if err != nil {
				t.Fatal(err)
			}
			data = append(data[:44], make([]byte, 48000*4*20)...)
			binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
			binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
			if err := os.WriteFile(audio, data, 0600); err != nil {
				t.Fatal(err)
			}
			yt := executable(t, "yt-dlp", fmt.Sprintf("echo $$ > '%s'\ncat '%s'\nexec sleep 60\n", ytPID, audio))
			decoder := executable(t, "ffmpeg", fmt.Sprintf("echo $$ > '%s'\nexec '%s' \"$@\"\n", ffPID, ff))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v := &brokenStreamVoice{failBeforeFrame: before}
			started := time.Now()
			err = (Streamer{YTDLP: yt, FFmpeg: decoder}).Play(ctx, media.Track{URL: "https://youtu.be/abcdefghijk"}, v)
			if !IsVoiceFailure(err) || time.Since(started) > 3*time.Second {
				t.Fatalf("transport failure/cleanup: %v", err)
			}
			for _, file := range []string{ytPID, ffPID} {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
					t.Fatalf("audio process %d not reaped: %v", pid, err)
				}
			}
		})
	}
}
