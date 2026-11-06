package player

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"whiterun/internal/media"
	"whiterun/internal/spotifyconnect"
)

type spotifyTestVoice struct{ frames atomic.Int32 }

func (v *spotifyTestVoice) Speaking(context.Context, bool) error     { return nil }
func (v *spotifyTestVoice) WriteFrame(context.Context, []byte) error { v.frames.Add(1); return nil }
func (v *spotifyTestVoice) Close(context.Context)                    {}
func TestSpotifyPCMIntegration(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg unavailable")
	}
	for _, scenario := range []string{"complete", "wrong_identity", "disconnect", "takeover", "unexpected_eof", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			pipe := filepath.Join(dir, "pcm")
			if err := syscall.Mkfifo(pipe, 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var conn *websocket.Conn
			var playCount atomic.Int32
			emit := func(kind, uri string) {
				mu.Lock()
				defer mu.Unlock()
				if conn != nil {
					conn.WriteJSON(map[string]any{"type": kind, "data": map[string]string{"uri": uri}})
				}
			}
			id := "0123456789ABCDEFGHIJKL"
			uri := "spotify:track:" + id
			var writers sync.WaitGroup
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/events":
					c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					mu.Lock()
					conn = c
					mu.Unlock()
					defer c.Close()
					for {
						if _, _, err = c.ReadMessage(); err != nil {
							return
						}
					}
				case "/player/play":
					playCount.Add(1)
					if scenario == "wrong_identity" {
						emit("metadata", "spotify:track:1123456789ABCDEFGHIJKL")
						return
					}
					emit("metadata", uri)
					if scenario == "disconnect" {
						mu.Lock()
						conn.Close()
						mu.Unlock()
						return
					}
					if scenario == "takeover" {
						emit("inactive", uri)
						return
					}
					if scenario == "unexpected_eof" || scenario == "cancel" {
						return
					}
					writers.Add(1)
					go func() {
						defer writers.Done()
						f, err := os.OpenFile(pipe, os.O_WRONLY, 0600)
						if err != nil {
							return
						}
						defer f.Close()
						_, err = f.Write(make([]byte, 44100*2*2/5))
						if err == nil {
							emit("not_playing", uri)
							emit("not_playing", uri)
						}
					}()
				}
			}))
			defer server.Close()
			c, err := spotifyconnect.New(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			session := &SpotifySession{Client: c, Pipe: pipe}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if scenario == "cancel" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			v := &spotifyTestVoice{}
			var logs bytes.Buffer
			pidfile := filepath.Join(dir, "ffmpeg-pids")
			ff, _ := exec.LookPath("ffmpeg")
			ff = executable(t, "ffmpeg", fmt.Sprintf("echo $$ >> %q\nexec %q \"$@\"\n", pidfile, ff))
			streamer := Streamer{FFmpeg: ff, YTDLP: "/must-not-run", Spotify: session, FirstAudioTimeout: time.Second, ResolveTimeout: time.Second}
			err = streamer.Play(ctx, media.Track{SpotifyID: id, RequestedSource: "spotify_connect", Duration: 200 * time.Millisecond, Timing: media.NewTiming(slog.New(slog.NewTextHandler(&logs, nil)))}, v)
			if scenario == "complete" {
				if strings.Count(logs.String(), "spotify_track_completed") != 1 {
					t.Fatal("completion not exactly once", logs.String())
				}
				if err != nil || v.frames.Load() == 0 {
					t.Fatal(err, v.frames.Load())
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if scenario != "complete" && v.frames.Load() != 0 {
				t.Fatal("unvalidated audio transmitted")
			}
			if scenario == "complete" || scenario == "unexpected_eof" {
				assertLocalReaped(t, pidfile)
			}
			if session.Busy() {
				t.Fatal("lease leaked")
			}
			if playCount.Load() != 1 {
				t.Fatal("duplicate play")
			}
			writers.Wait()
		})
	}
}
func TestSpotifyPipePermissions(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("synthetic"), 0600)
	if f, err := openSpotifyPipe(file); err == nil {
		f.Close()
		t.Fatal("ordinary file accepted")
	}
	pipe := filepath.Join(dir, "pcm")
	syscall.Mkfifo(pipe, 0600)
	symlink := filepath.Join(dir, "link")
	os.Symlink(pipe, symlink)
	if f, err := openSpotifyPipe(symlink); err == nil {
		f.Close()
		t.Fatal("symlink accepted")
	}
	os.Chmod(dir, 0755)
	if f, err := openSpotifyPipe(pipe); err == nil {
		f.Close()
		t.Fatal("public directory accepted")
	}
}

func TestSpotifyFailuresPreserveQueueWithoutRetry(t *testing.T) {
	for _, category := range []string{"spotify_session_expired", "spotify_identity_mismatch", "spotify_device_taken_over", "spotify_connection_lost", "spotify_pcm_process", "spotify_unconfirmed_completion", "audio_stall"} {
		t.Run(category, func(t *testing.T) {
			var calls atomic.Int32
			p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
				calls.Add(1)
				if err := v.WriteFrame(ctx, []byte{1}); err != nil {
					return err
				}
				return &StreamError{category, io.ErrUnexpectedEOF}
			})
			tr := media.Track{SpotifyID: "0123456789ABCDEFGHIJKL", RequestedSource: "spotify_connect", Duration: time.Minute}
			if err := p.EnqueueMany([]media.Track{tr}, p.Ticket()); err != nil {
				t.Fatal(err)
			}
			await(t, func() bool { return p.State().Halted })
			state := p.State()
			if len(state.Queued) != 1 {
				t.Fatalf("queue lost: %+v", state)
			}
			if state.Queued[0].SpotifyID != tr.SpotifyID || state.Queued[0].ResumeOffset != 20*time.Millisecond {
				t.Fatal(state.Queued)
			}
			time.Sleep(30 * time.Millisecond)
			if calls.Load() != 1 {
				t.Fatal("automatic replay", calls.Load())
			}
		})
	}
}

func TestSpotifyStartupPauseBudget(t *testing.T) {
	gate := &pauseGate{}
	gate.set(true)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), pauseKey{}, gate))
	defer cancel()
	resolved, first := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		watchSpotifyStartup(ctx, func(e error) { failed <- e }, resolved, first, 20*time.Millisecond, 20*time.Millisecond)
	}()
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-failed:
		t.Fatal("pause consumed startup budget", err)
	default:
	}
	close(resolved)
	close(first)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor leaked")
	}
}

func TestSpotifyConcurrentStartsHaveSingleOwner(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	pipe := filepath.Join(dir, "pcm")
	syscall.Mkfifo(pipe, 0600)
	var mu sync.Mutex
	var conn *websocket.Conn
	var plays atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			mu.Lock()
			conn = c
			mu.Unlock()
			defer c.Close()
			c.ReadMessage()
		case "/player/play":
			plays.Add(1)
			mu.Lock()
			conn.WriteJSON(map[string]any{"type": "metadata", "data": map[string]string{"uri": "spotify:track:0123456789ABCDEFGHIJKL"}})
			mu.Unlock()
		}
	}))
	defer server.Close()
	client, _ := spotifyconnect.New(server.URL)
	defer client.Close()
	session := &SpotifySession{Client: client, Pipe: pipe}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	type result struct {
		r   *spotifyRun
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			r, e := session.start(ctx, media.Track{SpotifyID: "0123456789ABCDEFGHIJKL", Timing: media.NewTiming(slog.New(slog.NewTextHandler(io.Discard, nil)))}, cancel)
			results <- result{r, e}
		}()
	}
	var owner *spotifyRun
	busy := 0
	for range 2 {
		select {
		case v := <-results:
			if v.err != nil {
				if failureCategory(v.err) != "spotify_session_busy" {
					t.Fatal(v.err)
				}
				busy++
			} else {
				owner = v.r
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent start hung")
		}
	}
	if owner == nil || busy != 1 || plays.Load() != 1 {
		t.Fatal("duplicate session", busy, plays.Load())
	}
	owner.close()
	if session.Busy() {
		t.Fatal("lease not released")
	}
}

func TestSpotifyPauseResumeCommandsDuringStartup(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	pipe := filepath.Join(dir, "pcm")
	syscall.Mkfifo(pipe, 0600)
	var mu sync.Mutex
	var conn *websocket.Conn
	requested := make(chan struct{})
	release := make(chan struct{})
	paused := make(chan struct{}, 2)
	resumed := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if e != nil {
				return
			}
			mu.Lock()
			conn = c
			mu.Unlock()
			defer c.Close()
			c.ReadMessage()
		case "/player/play":
			close(requested)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			mu.Lock()
			conn.WriteJSON(map[string]any{"type": "metadata", "data": map[string]string{"uri": "spotify:track:0123456789ABCDEFGHIJKL"}})
			mu.Unlock()
		case "/player/pause":
			paused <- struct{}{}
		case "/player/resume":
			resumed <- struct{}{}
		}
	}))
	defer server.Close()
	client, _ := spotifyconnect.New(server.URL)
	defer client.Close()
	session := &SpotifySession{Client: client, Pipe: pipe}
	gate := &pauseGate{}
	ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), pauseKey{}, gate))
	defer cancel(context.Canceled)
	done := make(chan *spotifyRun, 1)
	fail := make(chan error, 1)
	go func() {
		r, e := session.start(ctx, media.Track{SpotifyID: "0123456789ABCDEFGHIJKL", Timing: media.NewTiming(slog.New(slog.NewTextHandler(io.Discard, nil)))}, cancel)
		if e != nil {
			fail <- e
			return
		}
		done <- r
	}()
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("play not requested")
	}
	gate.set(true)
	close(release)
	var run *spotifyRun
	select {
	case run = <-done:
	case e := <-fail:
		t.Fatal(e)
	case <-time.After(time.Second):
		t.Fatal("startup hung")
	}
	defer run.close()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("actual source was not paused")
	}
	gate.set(true) // duplicate UI command is coalesced
	gate.set(false)
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("actual source was not resumed")
	}
	select {
	case <-paused:
		t.Fatal("duplicate pause command")
	default:
	}
}

func TestSpotifySourcePauseAcknowledgementAvoidsBackpressureDeadlock(t *testing.T) {
	gate := &pauseGate{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), pauseKey{}, gate))
	defer cancel()
	if gate.sourcePause() {
		t.Fatal("unexpected initial pause")
	}
	gate.set(true)
	// Source Pause must still be able to drain its blocked FIFO writer.
	if waited, err := waitPlayback(ctx); waited || err != nil {
		t.Fatal("delivery blocked before source pause acknowledgement")
	}
	gate.sourceAcknowledged(true)
	result := make(chan error, 1)
	go func() { _, err := waitPlayback(ctx); result <- err }()
	gate.set(false)
	select {
	case <-result:
		t.Fatal("delivery resumed before source acknowledgement")
	default:
	}
	gate.sourceAcknowledged(false)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resume acknowledgement did not release delivery")
	}
}
