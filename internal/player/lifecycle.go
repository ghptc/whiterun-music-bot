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

type AttemptState string

const (
	Pending   AttemptState = "PENDING"
	Resolving AttemptState = "RESOLVING"
	Ready     AttemptState = "READY"
	Starting  AttemptState = "STARTING"
	Playing   AttemptState = "PLAYING"
	Finished  AttemptState = "FINISHED"
	Failed    AttemptState = "FAILED"
	Canceled  AttemptState = "CANCELED"
)

// PlaybackEvent is a copied snapshot. Start events follow the first successful
// voice frame; skip events carry SkippedReason and never announce a start.
type PlaybackEvent struct {
	StartedAt     time.Time
	State         AttemptState
	FailureStage  string
	SkippedReason string
	Track         media.Track
	Next          *media.Track
	NextPending   bool
	Sequence      uint64
}
type startVoice struct {
	Voice
	once     sync.Once
	start    func()
	metadata func(media.Track)
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
	startedAt := time.Now()
	p.mu.Lock()
	if ctx.Err() != nil || p.current == nil || p.sequence != sequence || p.startedSequence == sequence {
		p.mu.Unlock()
		return
	}
	track = copyTrack(*p.current)
	p.startedSequence = sequence
	p.history = append(p.history, copyTrack(track))
	if len(p.history) > 100 {
		p.history = append([]media.Track(nil), p.history[len(p.history)-100:]...)
	}
	event := PlaybackEvent{StartedAt: startedAt, Track: copyTrack(track), Sequence: sequence, State: Playing}
	if len(p.queue) > 0 {
		next := copyTrack(p.queue[0].Track)
		event.Next = &next
		event.NextPending = p.queue[0].Pending
	}
	transitionMS := int64(0)
	if !p.lastTransition.IsZero() {
		transitionMS = time.Since(p.lastTransition).Milliseconds()
	}
	fn := p.onStart
	p.persistLocked()
	if recorder, ok := p.store.(PlaybackRecorder); ok && p.ctx.Err() == nil {
		save, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
		if err := recorder.RecordPlayback(save, p.guildID, track); err != nil {
			p.persistenceError = "Playback history could not be saved."
			p.log.Error("playback_history_write_failed", "guild_id", p.guildID, "error", err)
		}
		cancel()
	}
	p.mu.Unlock()
	p.log.Info("next_track_start", "state", "playing", "next_track_delay_ms", transitionMS, "spotify_id", track.SpotifyID, "sequence", sequence, "video_id", track.VideoID, "position", track.Position)
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

func (p *GuildPlayer) SetOnSkip(fn func(PlaybackEvent)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onSkip = fn
}
func (p *GuildPlayer) announceSkip(t media.Track, err error) {
	p.announceFailure(t, err, "resolution", 0)
}
func (p *GuildPlayer) announceFailure(t media.Track, err error, stage string, sequence uint64) {
	p.mu.Lock()
	fn := p.onSkip
	p.mu.Unlock()
	reason := "Search failed; the recording could not be verified."
	if stage == "playing" || stage == "starting" {
		reason = "Audio unavailable; playback could not complete."
	}
	var match *media.MatchError
	switch {
	case providerFailureCategory(err) != "":
		reason = "External provider blocked access to audio."
	case errors.As(err, &match):
		if match.Status == media.MatchUncertain {
			reason = "Ambiguous recording or insufficient metadata; no reliable selection."
		} else {
			reason = "Search found no compatible recording."
		}
	case errors.Is(err, context.DeadlineExceeded):
		reason = "Search timed out."
	default:
		var stream *StreamError
		if errors.As(err, &stream) {
			reason = "Audio unavailable; playback could not complete."
		}
	}
	if fn != nil {
		fn(PlaybackEvent{Track: copyTrack(t), SkippedReason: reason, FailureStage: stage, Sequence: sequence, State: Failed})
	}
}

// IsCurrentPlayback suppresses queued announcements superseded by skip/stop.
func (p *GuildPlayer) IsCurrentPlayback(sequence uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current != nil && p.sequence == sequence && p.startedSequence == sequence
}

// SetPlaybackTrack accepts final locator/display metadata before audio begins.
func (v *startVoice) SetPlaybackTrack(t media.Track) {
	if v.metadata != nil {
		v.metadata(t)
	}
}
