package player

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

// A decoder can finish valid audio while the source process remains alive.
// Previously ff.Wait succeeded, then <-ytDone waited forever after startup's
// watchdog had retired. Current remained set until an unrelated cancellation.
func TestSourceSurvivesAudioEOFCleanup(t *testing.T) {
	audio := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{1}, []byte{1})...)
	file := filepath.Join(t.TempDir(), "audio.ogg")
	if err := os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	yt := executable(t, "yt-dlp", "printf x; sleep 60")
	ff := executable(t, "ffmpeg", "cat '"+file+"'")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := (Streamer{YTDLP: yt, FFmpeg: ff, DrainTimeout: 30 * time.Millisecond}).Play(ctx, media.Track{}, &fakeVoice{})
	if ctx.Err() != nil {
		t.Fatalf("playback stuck after audio EOF until parent cancellation: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "source_after_eof") {
		t.Fatalf("missing lifecycle failure: %v", err)
	}
}

func TestPipelineSoak125Transitions(t *testing.T) {
	beforeGoroutines := runtime.NumGoroutine()
	dir := t.TempDir()
	file := filepath.Join(dir, "audio.ogg")
	audio := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{1}, []byte{1})...)
	if err := os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	pids := filepath.Join(dir, "pids")
	yt := executable(t, "yt-dlp", `echo $$ >> '`+pids+`'
for arg do target="$arg"; done
case "$target" in
ytfail) echo 'HTTP Error 403: Forbidden' >&2; exit 7;;
fffail) printf 'fffail\n'; cat '`+file+`';;
*) printf 'ok\n'; cat '`+file+`';;
esac`)
	ff := executable(t, "ffmpeg", `echo $$ >> '`+pids+`'
read mode
if [ "$mode" = fffail ]; then echo 'decoder failure' >&2; exit 8; fi
cat`)
	var logs lockedLog
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	stream := Streamer{YTDLP: yt, FFmpeg: ff}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p := New(ctx, log, stream.Play)
	if err := p.Attach(&fakeVoice{}); err != nil {
		t.Fatal(err)
	}
	defer p.Close(context.Background())
	tracks := numberedTracks(1, 125)
	failures, rejectedSources := 0, 0
	for i := range tracks {
		tracks[i].URL = "ok"
		if i%17 == 8 {
			tracks[i].URL = "ytfail"
			failures++
			rejectedSources++
		} else if i%23 == 9 {
			tracks[i].URL = "fffail"
			failures++
		}
	}
	var announced atomic.Int32
	p.SetOnStart(func(PlaybackEvent) { announced.Add(1) })
	beforeFD, _ := os.ReadDir("/proc/self/fd")
	if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	for {
		state := p.State()
		if state.Current == nil && len(state.Entries) == 0 {
			break
		}
		if state.Halted {
			t.Fatal("isolated failures halted queue")
		}
		select {
		case <-ctx.Done():
			t.Fatal("soak stuck", state, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	p.Close(context.Background())
	if announced.Load() != int32(125-failures) {
		t.Fatalf("announcements=%d expected=%d", announced.Load(), 125-failures)
	}
	text := logs.String()
	for _, event := range []string{"next_track_selected", "track_playback_start", "track_playback_end", "player_transition", "audio_source_exit"} {
		if n := strings.Count(text, `"msg":"`+event+`"`); n != 125 {
			t.Fatalf("%s count=%d", event, n)
		}
	}
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 250-rejectedSources {
		t.Fatalf("process count=%d", len(ids))
	}
	for _, id := range ids {
		pid, _ := strconv.Atoi(id)
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Fatalf("process %d not reaped: %v", pid, err)
		}
	}
	await(t, func() bool { return runtime.NumGoroutine() <= beforeGoroutines+1 })
	afterFD, _ := os.ReadDir("/proc/self/fd")
	if len(afterFD) > len(beforeFD)+1 {
		t.Fatalf("file descriptors accumulated: %d -> %d", len(beforeFD), len(afterFD))
	}
}

type lockedLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Write(p)
}
func (l *lockedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.data.String() }

func TestConsecutiveFailuresDrainWithoutHalt(t *testing.T) {
	var attempts atomic.Int32
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, v Voice) error {
		n := attempts.Add(1)
		if n <= 5 {
			return &StreamError{"yt_dlp_exit", errors.New("injected")}
		}
		return nil
	})
	if err := p.EnqueueMany(numberedTracks(1, 8), p.Ticket()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return attempts.Load() == 8 && p.State().Current == nil })
	if p.State().Halted || p.Length() != 0 {
		t.Fatal(p.State())
	}
}

func TestNaturalEOFRacesWithSkipAndStop(t *testing.T) {
	for _, operation := range []string{"skip", "stop"} {
		t.Run(operation, func(t *testing.T) {
			for range 30 {
				started, eof := make(chan struct{}), make(chan struct{})
				var successors atomic.Int32
				p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
					if track.Title == "current" {
						close(started)
						select {
						case <-eof:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					successors.Add(1)
					<-ctx.Done()
					return ctx.Err()
				})
				if err := p.EnqueueMany([]media.Track{{Title: "current"}, {Title: "next"}}, p.Ticket()); err != nil {
					t.Fatal(err)
				}
				<-started
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); close(eof) }()
				go func() {
					defer wg.Done()
					if operation == "skip" {
						p.SkipN(1)
					} else {
						p.Stop()
					}
				}()
				wg.Wait()
				p.Close(context.Background())
				if successors.Load() > 1 || p.State().Current != nil || p.Length() != 0 {
					t.Fatal("duplicate/stuck transition", p.State())
				}
			}
		})
	}
}
func TestProgressiveWorkerPanicFailsPositionAndAdvances(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	j := begin(t, p, context.Background(), numberedTracks(1, 5), func(_ context.Context, track media.Track, _ bool) (media.Track, error) {
		if track.Title == "3" {
			panic("injected matcher panic")
		}
		return matched(track), nil
	})
	firstResult(t, j)
	jobDone(t, j)
	await(t, func() bool { return p.State().Current != nil })
	state := p.State()
	if len(state.Entries) != 3 || state.Entries[1].Track.Title != "4" {
		t.Fatal(state)
	}
}

func TestAudioStallAndDecoderAfterEOF(t *testing.T) {
	for _, mode := range []string{"stall", "decoder_after_eof"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "audio.ogg")
			audio := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{1}, []byte{1})...)
			if err := os.WriteFile(file, audio, 0600); err != nil {
				t.Fatal(err)
			}
			body := "cat '" + file + "'; sleep 60"
			if mode == "decoder_after_eof" {
				body = "cat '" + file + "'; exec 1>&-; sleep 60"
			}
			yt := executable(t, "yt-dlp", "printf x; sleep 60")
			ff := executable(t, "ffmpeg", body)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (Streamer{YTDLP: yt, FFmpeg: ff, IdleTimeout: 80 * time.Millisecond, DrainTimeout: 20 * time.Millisecond}).Play(ctx, media.Track{Live: true}, &fakeVoice{})
			want := "audio_stall"
			if mode != "stall" {
				want = mode
			}
			if ctx.Err() != nil || failureCategory(err) != want {
				t.Fatal("unbounded/wrong failure", err, ctx.Err())
			}
		})
	}
}
func TestActivityDeadlineHonorsPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := &pauseGate{}
		ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), pauseKey{}, gate))
		defer cancel(nil)
		var last atomic.Int64
		last.Store(time.Now().UnixNano())
		done := make(chan struct{})
		go func() { defer close(done); watchActivity(ctx, cancel, &last, time.Second) }()
		gate.set(true)
		time.Sleep(5 * time.Second)
		if ctx.Err() != nil {
			t.Fatal("pause consumed idle deadline")
		}
		gate.set(false)
		time.Sleep(2 * time.Second)
		<-done
		if failureCategory(context.Cause(ctx)) != "audio_stall" {
			t.Fatal(context.Cause(ctx))
		}
	})
}

type panicVoice struct{ fakeVoice }

func (*panicVoice) WriteFrame(context.Context, []byte) error { panic("injected") }
func TestPanicReapsPipeline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.ogg")
	audio := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{1}, []byte{1})...)
	if err := os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	yt := executable(t, "yt-dlp", "cat '"+file+"'; sleep 60")
	ff := executable(t, "ffmpeg", "cat")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{}, &panicVoice{})
	if ctx.Err() != nil || failureCategory(err) != "playback_panic" {
		t.Fatal(err, ctx.Err())
	}
}

// The source leader can exit while a helper still owns its stdout. os/exec's
// context watcher has already retired then; WaitDelay=0 must not leak the helper.
func TestExitedSourceLeaderCannotLeakPipeHoldingHelper(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.ogg")
	audio := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{1}, []byte{1})...)
	if err := os.WriteFile(file, audio, 0600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	yt := executable(t, "yt-dlp", "cat '"+file+"'; sleep 60 &\necho $! > '"+pidFile+"'\n")
	ff := executable(t, "ffmpeg", "cat")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{}, &fakeVoice{}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(400 * time.Millisecond):
		raw, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		<-done
		t.Fatal("exited source leader left a helper and a stuck stdout copier after cancellation")
	}
}
