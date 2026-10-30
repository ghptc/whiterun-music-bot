package player

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"whiterun/internal/media"
)

func localFixture(t *testing.T) (Streamer, media.Track) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("local audio acceptance requires FFmpeg:", err)
	}
	root, err := filepath.Abs("testdata/local-audio")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := media.LocalTrack("validation.flac", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr.Timing = media.NewTiming(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	return Streamer{YTDLP: "/must-never-run-yt-dlp", FFmpeg: ff, LocalAudioDir: root}, tr
}

type localVoice struct {
	fakeVoice
	frames  atomic.Int64
	first   chan struct{}
	once    sync.Once
	onFirst func()
}

func (v *localVoice) WriteFrame(_ context.Context, p []byte) error {
	if len(p) == 0 {
		return errors.New("empty packet")
	}
	v.frames.Add(1)
	v.once.Do(func() {
		if v.onFirst != nil {
			v.onFirst()
		}
		if v.first != nil {
			close(v.first)
		}
	})
	return nil
}

func waitLocal(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("local audio transition timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLocalAudioFullPipelineEOF(t *testing.T) {
	s, tr := localFixture(t)
	var logs bytes.Buffer
	tr.Timing = media.NewTiming(slog.New(slog.NewJSONHandler(&logs, nil)))
	pidfile := filepath.Join(t.TempDir(), "pids")
	s.FFmpeg = executable(t, "ffmpeg", fmt.Sprintf("echo $$ >> %q\nexec %q \"$@\"\n", pidfile, s.FFmpeg))
	v := &localVoice{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	started := time.Now()
	if err := s.Play(ctx, tr, v); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	assertLocalReaped(t, pidfile)
	if elapsed < 29*time.Second || elapsed > 38*time.Second || v.frames.Load() < 1500 || v.frames.Load() > 1510 {
		t.Fatalf("elapsed=%s frames=%d", elapsed, v.frames.Load())
	}
	for _, event := range []string{"local_audio_opened", "local_audio_first_frame", "local_audio_completed"} {
		if strings.Count(logs.String(), `"msg":"`+event+`"`) != 1 {
			t.Fatalf("missing or duplicate %s", event)
		}
	}
	if strings.Contains(logs.String(), `"msg":"local_audio_failed"`) || strings.Contains(logs.String(), "yt_dlp_start") || strings.Contains(logs.String(), s.LocalAudioDir) {
		t.Fatal("failure, provider invocation or path leaked")
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == "local_audio_completed" {
			if event["progress_ms"].(float64) < 30000 || event["local_correlation_id"] == nil || event["first_frame_ms"].(float64) <= 0 {
				t.Fatal(event)
			}
			t.Logf("completed: duration_ms=%v progress_ms=%v first_frame_ms=%v frames=%d elapsed=%s", event["duration_ms"], event["progress_ms"], event["first_frame_ms"], v.frames.Load(), elapsed)
		}
	}
}

func TestLocalAudioPathSecurity(t *testing.T) {
	s, _ := localFixture(t)
	root := t.TempDir()
	good, err := os.ReadFile(filepath.Join(s.LocalAudioDir, "validation.flac"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), b, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("valid.flac", good, 0600)
	write("denied.flac", good, 0000)
	write("invalid.flac", []byte("not audio"), 0600)
	write("wrong.wav", good, 0600)
	write("unsupported.mp3", good, 0600)
	if err := os.Symlink(filepath.Join(s.LocalAudioDir, "validation.flac"), filepath.Join(root, "outside.flac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("valid.flac", filepath.Join(root, "inside.flac")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.flac"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"missing.flac", "invalid.flac", "wrong.wav", "unsupported.mp3", "denied.flac", "outside.flac", "inside.flac", "fifo.flac", "../validation.flac", "/etc/passwd", "a;id.flac", "$(id).flac", "sub/valid.flac"} {
		t.Run(id, func(t *testing.T) {
			f, _, err := openLocalAudio(root, id)
			if f != nil {
				f.Close()
			}
			if err == nil {
				t.Fatal("unsafe source accepted")
			}
			if strings.Contains(err.Error(), root) {
				t.Fatal("sensitive path leaked")
			}
		})
	}
	f, format, err := openLocalAudio(root, "valid.flac")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if format != "flac" {
		t.Fatal(format)
	}
	if err := os.Remove(filepath.Join(root, "valid.flac")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(f); err != nil {
		t.Fatal("held descriptor lost after unlink:", err)
	}
	if _, _, err := openLocalAudio("relative", "valid.flac"); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestLocalAudioGuildControls(t *testing.T) {
	s, tr := localFixture(t)
	v := &localVoice{first: make(chan struct{})}
	p := New(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), s.Play)
	defer p.Close(context.Background())
	if err := p.Attach(v); err != nil {
		t.Fatal(err)
	}
	if err := p.EnqueueMany([]media.Track{tr, tr, tr}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.first:
	case <-time.After(5 * time.Second):
		t.Fatal("no first frame")
	}
	waitLocal(t, func() bool { return localProgress(p) >= 100*time.Millisecond })
	if _, err := p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	before := v.frames.Load()
	progress := localProgress(p)
	time.Sleep(150 * time.Millisecond)
	after := p.State()
	if v.frames.Load() != before || localProgress(p) != progress || len(after.Queued) != 2 || !after.Paused {
		t.Fatal("pause changed frames/progress/queue")
	}
	if _, err := p.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	waitLocal(t, func() bool { return v.frames.Load() > before+3 })
	if !p.Skip() {
		t.Fatal("skip rejected")
	}
	waitLocal(t, func() bool { state := p.State(); return state.Current != nil && len(state.Queued) == 1 })
	if len(p.History()) > 2 {
		t.Fatal("duplicate queue advancement")
	}
	p.Stop()
	waitLocal(t, func() bool { state := p.State(); return state.Current == nil && len(state.Queued) == 0 })
	// Stop keeps its existing explicit queue-clearing semantics; it must not Leave.
	v.mu.Lock()
	closed := v.closed
	v.mu.Unlock()
	if closed {
		t.Fatal("control unexpectedly closed Voice")
	}
}

func assertLocalReaped(t *testing.T, pidfile string) {
	t.Helper()
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		t.Fatal("no subprocess observed")
	}
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("unreaped subprocess %d: %v", pid, err)
		}
	}
}

func TestLocalAudioCancellationRemovalAndProcessCleanup(t *testing.T) {
	for _, action := range []string{"cancel", "remove", "resume"} {
		t.Run(action, func(t *testing.T) {
			s, tr := localFixture(t)
			root := t.TempDir()
			audio, err := os.ReadFile(filepath.Join(s.LocalAudioDir, tr.URL))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, tr.URL)
			if err := os.WriteFile(file, audio, 0600); err != nil {
				t.Fatal(err)
			}
			s.LocalAudioDir = root
			pidfile := filepath.Join(t.TempDir(), "pids")
			s.FFmpeg = executable(t, "ffmpeg", fmt.Sprintf("echo $$ >> %q\nexec %q \"$@\"\n", pidfile, s.FFmpeg))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if action != "cancel" {
				tr.ResumeOffset = 29500 * time.Millisecond
			}
			gate := &pauseGate{}
			ctx = context.WithValue(ctx, pauseKey{}, gate)
			v := &localVoice{first: make(chan struct{}), onFirst: func() {
				if action == "cancel" {
					gate.set(true)
				}
			}}
			done := make(chan error, 1)
			go func() { done <- s.Play(ctx, tr, v) }()
			select {
			case <-v.first:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if action == "remove" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if action == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if action != "cancel" {
					if err != nil || v.frames.Load() < 25 {
						t.Fatal(err, v.frames.Load())
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pipeline hung")
			}
			assertLocalReaped(t, pidfile)
		})
	}
}

func TestLocalAudioUnexpectedFFmpegExit(t *testing.T) {
	s, tr := localFixture(t)
	realFF := s.FFmpeg
	pidfile := filepath.Join(t.TempDir(), "pids")
	s.FFmpeg = executable(t, "ffmpeg", fmt.Sprintf("echo $$ >> %q\ncase \" $* \" in *' -c:a libopus '*) exit 9;; esac\nexec %q \"$@\"\n", pidfile, realFF))
	var logs bytes.Buffer
	tr.Timing = media.NewTiming(slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := s.Play(ctx, tr, &localVoice{}); err == nil {
		t.Fatal("decoder exit accepted")
	}
	if strings.Count(logs.String(), `"msg":"local_audio_failed"`) != 1 || strings.Contains(logs.String(), `"msg":"local_audio_completed"`) {
		t.Fatal("invalid terminal events")
	}
	assertLocalReaped(t, pidfile)
}

func localProgress(p *GuildPlayer) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.confirmedOffset
}

func TestLocalAudioWAV(t *testing.T) {
	s, tr := localFixture(t)
	file := wav(t)
	s.LocalAudioDir = filepath.Dir(file)
	tr.URL = filepath.Base(file)
	tr.Duration = 100 * time.Millisecond
	if err := s.Play(context.Background(), tr, &localVoice{}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAudioCorruptAndMissingTerminal(t *testing.T) {
	for _, name := range []string{"missing.flac", "corrupt.flac"} {
		t.Run(name, func(t *testing.T) {
			s, tr := localFixture(t)
			s.LocalAudioDir = t.TempDir()
			tr.URL = name
			if name == "corrupt.flac" {
				if err := os.WriteFile(filepath.Join(s.LocalAudioDir, name), append([]byte("fLaC"), make([]byte, 30)...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			tr.Timing = media.NewTiming(slog.New(slog.NewJSONHandler(&logs, nil)))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.Play(ctx, tr, &localVoice{}); err == nil {
				t.Fatal("invalid audio accepted")
			}
			if strings.Count(logs.String(), `"msg":"local_audio_failed"`) != 1 || strings.Contains(logs.String(), s.LocalAudioDir) {
				t.Fatal("missing failure or sensitive path exposed")
			}
		})
	}
}
