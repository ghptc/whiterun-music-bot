package player

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
)

func TestREL024HealthDoesNotWaitForDeadlockedWorker(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	p.mu.Lock()
	started := time.Now()
	h := p.Health()
	p.mu.Unlock()
	if h.Responsive || h.State != "lock_unresponsive" || time.Since(started) > time.Second {
		t.Fatal(h)
	}
	// The worker may legitimately acquire the mutex immediately after Unlock.
	// Verify eventual responsiveness, not an uncontended scheduling instant.
	deadline := time.Now().Add(time.Second)
	for !p.Health().Responsive {
		if time.Now().After(deadline) {
			t.Fatal("recovered lock still unhealthy")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestREL024TransientNetworkPreservesPositionsAndHasFiniteBudget(t *testing.T) {
	for _, recoverable := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent", true: "transient"}[recoverable], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls, notices atomic.Int32
				p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
					n := calls.Add(1)
					if track.Position != 1 {
						t.Error("network outage advanced queue", track.Position)
					}
					if !recoverable || n < 3 {
						return &StreamError{"source_network", errors.New("injected DNS outage")}
					}
					if err := v.WriteFrame(ctx, []byte{1}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				})
				p.SetOnSkip(func(PlaybackEvent) { notices.Add(1) })
				if err := p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(10 * time.Second)
				synctest.Wait()
				s := p.State()
				if calls.Load() != 3 || notices.Load() != 0 {
					t.Fatal(calls.Load(), notices.Load())
				}
				if recoverable {
					if s.Current == nil || s.Current.Position != 1 || len(s.Entries) != 122 {
						t.Fatal(s)
					}
				} else {
					if s.Current != nil || len(s.Entries) != 123 || !p.halted {
						t.Fatal(s)
					}
					time.Sleep(time.Hour)
					synctest.Wait()
					if calls.Load() != 3 {
						t.Fatal("unbounded retries")
					}
				}
				t.Logf("calls=%d notifications=%d positions=123/123; recovered=%t", calls.Load(), notices.Load(), recoverable)
			})
		})
	}
}

// DNS is injected through a real failed extractor process. Recovery must cross
// the actual scheduler, OS pipes, FFmpeg/libopus and first successful frame.
func TestREL024NetworkRecoveryWithRealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "attempts")
	file := wav(t)
	yt := executable(t, "yt-dlp", fmt.Sprintf(`n=0
if [ -f '%s' ]; then read n < '%s'; fi
n=$((n+1)); echo "$n" > '%s'
if [ "$n" -lt 3 ]; then echo 'Temporary failure in name resolution' >&2; exit 7; fi
cat '%s'
`, counter, counter, counter, file))
	stream := Streamer{YTDLP: yt, FFmpeg: ff}
	var frameAt atomic.Int64
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
		err := stream.Play(ctx, tr, v)
		if err == nil {
			frameAt.Store(time.Now().UnixNano())
			<-ctx.Done()
			return ctx.Err()
		}
		return err
	})
	started := time.Now()
	if err = p.EnqueueMany(numberedTracks(1, 123), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for frameAt.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if frameAt.Load() == 0 {
		t.Fatal("real pipeline failed to recover")
	}
	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	s := p.State()
	if s.Current == nil || s.Current.Position != 1 || len(s.Entries) != 122 {
		t.Fatal(s)
	}
	t.Logf("real extractor exits=2; FFmpeg frames delivered; recovery=%s; positions=123/123", time.Unix(0, frameAt.Load()).Sub(started))
}

func TestREL024FrozenRealFFmpegIsKilledAndReaped(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(t.TempDir(), "ffmpeg.pid")
	wrapper := executable(t, "ffmpeg-supervised", "echo $$ > '"+pidfile+"'\nexec '"+ff+"' \"$@\"\n")
	yt := executable(t, "source", "cat '"+wav(t)+"'; sleep 60")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (Streamer{YTDLP: yt, FFmpeg: wrapper, FirstAudioTimeout: time.Second, IdleTimeout: time.Second, DrainTimeout: 20 * time.Millisecond}).Play(ctx, media.Track{}, &fakeVoice{})
	}()
	pid := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, e := os.ReadFile(pidfile)
		if e == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			target, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			if e == nil && strings.Contains(target, "ffmpeg") {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	target, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil || !strings.Contains(target, "ffmpeg") {
		t.Fatal("actual FFmpeg not running", target, e)
	}
	started := time.Now()
	if err = syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	err = <-done
	if ctx.Err() != nil || err == nil {
		t.Fatal("frozen decoder not supervised", err, ctx.Err())
	}
	if e = syscall.Kill(pid, 0); e != syscall.ESRCH {
		t.Fatal("stopped FFmpeg not killed/reaped", e)
	}
	t.Logf("real FFmpeg SIGSTOP: detection/cleanup=%s; pid reaped; error=%v", time.Since(started), err)
}
