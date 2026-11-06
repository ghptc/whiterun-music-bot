package player

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/spotifyconnect"
)

// SpotifySession is shared by all guild streamers: one account/device can own
// one stream. A failed cleanup poisons the lease until an operator restarts Bard.
type SpotifySession struct {
	Client   *spotifyconnect.Client
	Pipe     string
	mu       sync.Mutex
	active   bool
	poisoned bool
}

func (s *SpotifySession) Busy() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active || s.poisoned }
func spotifyError(err error) error {
	category := "spotify_connection_lost"
	switch {
	case errors.Is(err, spotifyconnect.ErrAuth):
		category = "spotify_auth_required"
	case errors.Is(err, spotifyconnect.ErrIdentity):
		category = "spotify_identity_mismatch"
	case errors.Is(err, spotifyconnect.ErrUnavailable):
		category = "spotify_unavailable"
	case errors.Is(err, spotifyconnect.ErrRateLimited):
		category = "spotify_rate_limited"
	case errors.Is(err, spotifyconnect.ErrRejected):
		category = "spotify_command_rejected"
	}
	return &StreamError{category, err}
}

type spotifyRun struct {
	file           *os.File
	completed      atomic.Bool
	completion     chan struct{}
	completionOnce sync.Once
	cancel         context.CancelFunc
	done           chan error
	controlsDone   chan struct{}
	closeOnce      sync.Once
	session        *SpotifySession
}

// Read drains the final FIFO bytes after the matching natural-completion event.
// EOF is a short quiet period after that event, never an arbitrary duration cut.
// Deadlines use Go's poller and reset at each read so downstream backpressure
// cannot expire a deadline while the final bytes are still buffered.
func (r *spotifyRun) Read(p []byte) (int, error) {
	if r.completed.Load() {
		r.file.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}
	n, err := r.file.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) && r.completed.Load() {
		return n, io.EOF
	}
	return n, err
}

func (r *spotifyRun) close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.file.Close()
		<-r.done
		<-r.controlsDone
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := r.session.Client.Stop(ctx)
		r.session.mu.Lock()
		r.session.poisoned = err != nil
		r.session.active = false
		r.session.mu.Unlock()
	})
}

// openSpotifyPipe rejects ordinary files and symlinks. O_RDWR prevents premature
// EOF before the daemon opens its writer. Completion is proved by events + the
// finite PCM frame budget; this descriptor is closed on every terminal path.
func openSpotifyPipe(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Spotify FIFO path must be absolute")
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Spotify FIFO directory must be private (0700)")
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("Spotify FIFO unavailable")
	}
	f := os.NewFile(uintptr(fd), "spotify-pcm")
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("Spotify FIFO must be a private named pipe (0600)")
	}
	// Discard bytes left by a prior canceled writer before issuing a new command.
	buf := make([]byte, 8192)
	for {
		_, err := syscall.Read(fd, buf)
		if err != nil {
			break
		}
	}
	return f, nil
}
func (s *SpotifySession) start(parent context.Context, t media.Track, cancelCause context.CancelCauseFunc) (run *spotifyRun, err error) {
	s.mu.Lock()
	if s.active || s.poisoned {
		s.mu.Unlock()
		return nil, &StreamError{"spotify_session_busy", errors.New("Spotify is occupied or requires operator recovery")}
	}
	s.active = true
	s.mu.Unlock()
	var f *os.File
	managed := false
	defer func() {
		if err != nil && !managed {
			if f != nil {
				f.Close()
			}
			s.mu.Lock()
			s.active = false
			s.mu.Unlock()
		}
	}()
	// Stop before opening/draining the FIFO, never reuse a previous writer.
	if err = s.Client.Stop(parent); err != nil {
		return nil, spotifyError(err)
	}
	t.Timing.Event("spotify_connect_authenticated", time.Now())
	if err = s.Client.SingleTrack(parent); err != nil {
		return nil, spotifyError(err)
	}
	f, err = openSpotifyPipe(s.Pipe)
	if err != nil {
		return nil, &StreamError{"spotify_pcm_open", err}
	}
	ctx, cancel := context.WithCancel(parent)
	run = &spotifyRun{file: f, cancel: cancel, done: make(chan error, 1), controlsDone: make(chan struct{}), completion: make(chan struct{}), session: s}
	context.AfterFunc(ctx, func() { f.Close() })
	managed = true
	r := run
	controlsReady := make(chan bool, 1)
	ready := make(chan struct{})
	identity := make(chan struct{})
	var identityOnce sync.Once
	uri := "spotify:track:" + t.SpotifyID
	go func() {
		err := s.Client.EventsReady(ctx, ready, func(event spotifyconnect.Event) error {
			if event.Data.PlayOrigin != "" && event.Data.PlayOrigin != "go-librespot" {
				return &StreamError{"spotify_device_taken_over", errors.New("Spotify playback changed outside Bard")}
			}
			switch event.Type {
			case "will_play", "metadata", "playing":
				if event.Data.URI != uri {
					return spotifyconnect.ErrIdentity
				}
				if event.Type == "metadata" || event.Type == "playing" {
					identityOnce.Do(func() { close(identity) })
				}
				if event.Type == "playing" {
					t.Timing.Event("spotify_track_started", time.Now(), "spotify_uri", uri)
				}
			case "not_playing":
				if event.Data.URI != uri {
					return spotifyconnect.ErrIdentity
				}
				r.completed.Store(true)
				r.file.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				r.completionOnce.Do(func() { close(r.completion) })
			case "stopped":
				select {
				case <-identity:
					if !r.completed.Load() {
						return &StreamError{"spotify_playback_interrupted", errors.New("Spotify stopped before completion")}
					}
				default:
				}
			case "repeat_track", "repeat_context", "shuffle_context":
				if event.Data.Value {
					return &StreamError{"spotify_context_changed", errors.New("Spotify queue options changed outside Bard")}
				}
			case "inactive":
				t.Timing.Event("spotify_device_taken_over", time.Now())
				return &StreamError{"spotify_device_taken_over", errors.New("Spotify device became inactive")}
			case "playback_error":
				category := "spotify_playback_interrupted"
				switch event.Data.Kind {
				case "network":
					category = "spotify_connection_lost"
				case "timeout":
					category = "spotify_timeout"
				case "rate_limited":
					category = "spotify_rate_limited"
				case "server":
					category = "spotify_server_failure"
				case "unsupported":
					category = "spotify_format_unsupported"
				}

				if event.Data.Unplayable {
					category = "spotify_track_unavailable"
				}
				if event.Data.Stage == "session" {
					category = "spotify_session_lost"
				}
				return &StreamError{category, errors.New("Spotify player reported playback failure")}
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			var stream *StreamError
			if !errors.As(err, &stream) {
				err = spotifyError(err)
			}
			cancelCause(err)
		}
		r.done <- err
	}()

	// Gate changes are event-driven and never perform network IO under player.mu.
	go func() {
		defer close(r.controlsDone)
		var paused bool
		select {
		case <-ctx.Done():
			return
		case paused = <-controlsReady:
		}
		gate, _ := parent.Value(pauseKey{}).(*pauseGate)
		if gate == nil {
			<-ctx.Done()
			return
		}
		for {
			next, changed := gate.snapshot()
			if next != paused {
				var e error
				if next {
					e = s.Client.Pause(ctx)
				} else {
					e = s.Client.Resume(ctx)
				}
				if e != nil {
					if ctx.Err() == nil {
						if errors.Is(e, spotifyconnect.ErrAuth) {
							t.Timing.Event("spotify_session_expired", time.Now())
							cancelCause(&StreamError{"spotify_session_expired", e})
						} else {
							cancelCause(spotifyError(e))
						}
					}
					return
				}
				paused = next
				gate.sourceAcknowledged(paused)
				name := "spotify_playback_resumed"
				if paused {
					name = "spotify_playback_paused"
				}
				t.Timing.Event(name, time.Now())
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}()

	fail := func(e error) (*spotifyRun, error) { run.close(); return nil, e }
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case <-ctx.Done():
		return fail(context.Cause(parent))
	case <-timer.C:
		return fail(&StreamError{"spotify_startup_timeout", errors.New("Spotify events handshake timed out")})
	}
	t.Timing.Event("spotify_track_requested", time.Now(), "spotify_uri", uri)

	paused := false
	if gate, _ := parent.Value(pauseKey{}).(*pauseGate); gate != nil {
		paused = gate.sourcePause()
	}
	if e := s.Client.PlayAt(ctx, uri, t.ResumeOffset.Milliseconds(), paused); e != nil {
		return fail(spotifyError(e))
	}
	controlsReady <- paused

	select {
	case <-identity:
	case <-ctx.Done():
		return fail(context.Cause(parent))
	case <-timer.C:
		return fail(&StreamError{"spotify_identity_timeout", errors.New("Spotify did not confirm requested recording")})
	}
	return run, nil
}

// Startup budgets count active time only. A pause during decoder startup must
// not turn an intentional lack of PCM into a failed track.
func watchSpotifyStartup(ctx context.Context, cancel context.CancelCauseFunc, resolved, first <-chan struct{}, resolveTimeout, audioTimeout time.Duration) {
	wait := func(signal <-chan struct{}, remaining time.Duration) bool {
		for {
			gate, _ := ctx.Value(pauseKey{}).(*pauseGate)
			var changed <-chan struct{}
			paused := false
			if gate != nil {
				paused, changed = gate.snapshot()
			}
			timer := time.NewTimer(remaining)
			var timeout <-chan time.Time
			if paused {
				timer.Stop()
			} else {
				timeout = timer.C
			}
			started := time.Now()
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-first:
				timer.Stop()
				return true
			case <-signal:
				timer.Stop()
				return true
			case <-timeout:
				cancel(&StreamError{"spotify_startup_timeout", errors.New("Spotify audio startup exceeded active deadline")})
				return false
			case <-changed:
				timer.Stop()
				if !paused {
					remaining = max(time.Nanosecond, remaining-time.Since(started))
				}
			}
		}
	}
	if wait(resolved, resolveTimeout) {
		wait(first, audioTimeout)
	}
}
