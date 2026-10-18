package player

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"whiterun/internal/media"
)

// StreamError separates failure categories from bounded, redacted diagnostics.
type StreamError struct {
	Category string
	Err      error
}

func (e *StreamError) Error() string { return e.Category + ": " + e.Err.Error() }
func (e *StreamError) Unwrap() error { return e.Err }
func failureCategory(err error) string {
	if err == nil {
		return "eof"
	}
	var stream *StreamError
	if errors.As(err, &stream) {
		return stream.Category
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "unexpected_cancellation"
	}
	return "playback_failure"
}

func isProviderFailure(err error) bool {
	return providerFailureCategory(err) != ""
}

func providerFailureCategory(err error) string {
	if err == nil {
		return ""
	}
	category := failureCategory(err)
	if category == "youtube_auth_challenge" || category == "youtube_rate_limited" || category == "youtube_provider_blocked" {
		return category
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "sign in to confirm"), strings.Contains(text, "confirm you're not a bot"), strings.Contains(text, "confirm you’re not a bot"), strings.Contains(text, "authentication required"), strings.Contains(text, "login required"), strings.Contains(text, "cookies are required"):
		return "youtube_auth_challenge"
	case strings.Contains(text, "too many requests"), strings.Contains(text, "rate limit"), strings.Contains(text, "http error 429"), strings.Contains(text, " 429 "):
		return "youtube_rate_limited"
	default:
		return ""
	}
}
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// A post-start stall must not leave Current forever. Pause suspends this idle
// deadline; there is no wall-clock song-length limit, including for live audio.
func watchActivity(ctx context.Context, cancel context.CancelCauseFunc, last *atomic.Int64, timeout time.Duration) {
	tick := time.NewTicker(max(time.Millisecond, min(time.Second, timeout/4)))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			gate, _ := ctx.Value(pauseKey{}).(*pauseGate)
			if gate.paused() {
				last.Store(now.UnixNano())
				continue
			}
			if now.Sub(time.Unix(0, last.Load())) > timeout {
				cancel(&StreamError{"audio_stall", errors.New("no audio progress within idle deadline")})
				return
			}
		}
	}
}

// PlaybackEvent is a copied snapshot emitted once after the first successful
// voice frame, never merely on resolution or queue insertion.
type PlaybackEvent struct {
	Track       media.Track
	Next        *media.Track
	NextPending bool
	Sequence    uint64
}
type startVoice struct {
	Voice
	once  sync.Once
	start func()
}

func (v *startVoice) WriteFrame(ctx context.Context, frame []byte) error {
	if err := v.Voice.WriteFrame(ctx, frame); err != nil {
		return err
	}
	v.once.Do(v.start)
	return nil
}
func (p *GuildPlayer) SetOnStart(fn func(PlaybackEvent)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onStart = fn
}
func (p *GuildPlayer) announceStart(ctx context.Context, track media.Track, sequence uint64) {
	p.mu.Lock()
	if ctx.Err() != nil || p.current == nil {
		p.mu.Unlock()
		return
	}
	event := PlaybackEvent{Track: copyTrack(track), Sequence: sequence}
	if len(p.queue) > 0 {
		next := copyTrack(p.queue[0].Track)
		event.Next = &next
		event.NextPending = p.queue[0].Pending
	}
	fn := p.onStart
	p.mu.Unlock()
	p.log.Info("next_track_start", "sequence", sequence, "video_id", track.VideoID, "position", track.Position)
	if fn != nil {
		fn(event)
	}
}
func callPlay(play PlayFunc, ctx context.Context, track media.Track, v Voice) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &StreamError{"playback_panic", fmt.Errorf("playback panic (%T)", value)}
		}
	}()
	return play(ctx, track, v)
}
func (p *GuildPlayer) queueCountsLocked() (ready, pending int) {
	for _, entry := range p.queue {
		if entry.Pending {
			pending++
		} else {
			ready++
		}
	}
	return
}
