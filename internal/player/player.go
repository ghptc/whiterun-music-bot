package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"whiterun/internal/media"
)

// Voice is the external transport boundary; tests use an in-memory transport.
type Voice interface {
	WriteFrame(context.Context, []byte) error
	Speaking(context.Context, bool) error
	Close(context.Context)
}
type PlayFunc func(context.Context, media.Track, Voice) error

const MaxQueueTracks = 1000

type GuildPlayer struct {
	networkIncident        string
	retryingPersistence    bool
	persistenceWorkers     sync.WaitGroup
	networkRecoveryStarted time.Time
	networkAttempts        int
	retryAt                time.Time
	canceledAt             time.Time
	progress               playbackProgress
	recoveryPaused         bool
	checkpointOffset       time.Duration
	checkpointDelayed      bool
	confirmedOffset        time.Duration

	diagnostics         *diagnostics
	queueVersion        uint64
	lastSignal          string
	sessionID           string
	loop                string
	autoplay            bool
	store               Store
	guildID             string
	persistenceError    string
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	done                chan struct{}
	wake                chan struct{}
	log                 *slog.Logger
	play                PlayFunc
	voice               Voice
	current             *media.Track
	history             []media.Track
	completedImports    []CollectionProgress
	lastTransition      time.Time
	startedSequence     uint64
	queue               []queueEntry
	jobs                map[uint64]*CollectionJob
	nextJob             uint64
	jobWorkers          sync.WaitGroup
	trackCancel         context.CancelFunc
	trackDone           chan struct{}
	generation          uint64
	pause               *pauseGate
	trackCause          context.CancelCauseFunc
	onStart             func(PlaybackEvent)
	onSkip              func(PlaybackEvent)
	sequence            uint64
	halted              bool
	failures            int
	voiceBlocked        bool
	voiceIncident       bool
	voiceIncidentID     string
	voiceAttempts       int
	voiceNotice         bool
	voiceEpoch          uint64
	voiceRecoveryCancel context.CancelFunc
	playbackProviderID  string
	providerProbe       bool
	providerRetries     int
	providerRetryAt     time.Time
	providerIncident    string
	providerGeneration  uint64
	providerNotice      bool
	providerBlocked     bool
	providerReason      string
	providerChanged     chan struct{}
}

func New(ctx context.Context, log *slog.Logger, play PlayFunc) *GuildPlayer {
	ctx, cancel := context.WithCancel(ctx)
	p := &GuildPlayer{ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), log: log, play: play, providerChanged: make(chan struct{})}
	p.sessionID = fmt.Sprintf("%d", time.Now().UnixNano())
	p.diagnostics = newDiagnostics(log)
	go p.run()
	return p
}

// signal callers hold mu (including notification provenance).
func (p *GuildPlayer) signal(reasons ...string) {
	p.lastSignal = "queue_changed"
	if len(reasons) > 0 {
		p.lastSignal = reasons[0]
	}
	p.diagnosticLocked(slog.LevelDebug, "scheduler_notification", "wakeup_reason", p.lastSignal)
	// A queue mutation can expose a pending head outside the old prefetch window.
	// Wake the owners of that window as well as playback; neither consumer can
	// substitute for the other. Notifications coalesce and never run resolver work
	// under this lock.
	for _, job := range p.jobs {
		select {
		case job.wake <- struct{}{}:
		default:
		}
	}

	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *GuildPlayer) Attach(v Voice) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return errors.New("The Bard is shutting down.")
	}
	if p.voice != nil {
		return errors.New("The Bard is already summoned.")
	}
	p.voice = v
	p.voiceBlocked = false
	p.voiceIncident = false
	p.voiceNotice = false
	p.voiceAttempts = 0
	p.generation++
	p.mutationLocked("generation_changed")
	p.signal()
	return nil
}
func (p *GuildPlayer) Ticket() uint64 { p.mu.Lock(); defer p.mu.Unlock(); return p.generation }
func (p *GuildPlayer) Enqueue(t media.Track, ticket uint64) error {
	return p.EnqueueMany([]media.Track{t}, ticket)
}

// EnqueueMany appends a collection atomically; concurrent commands cannot interleave it.
func (p *GuildPlayer) EnqueueMany(tracks []media.Track, ticket uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	if p.ctx.Err() != nil || p.voice == nil || ticket != p.generation {
		p.diagnosticLocked(slog.LevelInfo, "queue_generation_rejected", "requested_generation", ticket, "reason", "stale_or_canceled")
		return errors.New("Playback changed while searching. Ask the Bard again.")
	}
	if len(tracks) > MaxQueueTracks-len(p.queue) {
		return errors.New("The collection will not fit in the queue (1000 upcoming tracks maximum).")
	}
	p.halted = false
	p.failures = 0
	for i, track := range tracks {
		if track.Position == 0 {
			track.Position, track.Total = i+1, len(tracks)
		}
		p.queue = append(p.queue, queueEntry{Track: copyTrack(track), position: track.Position - 1})
	}
	p.mutationLocked("enqueue", "added_count", len(tracks))
	p.signal("queue_insert")
	return nil
}
func (p *GuildPlayer) Skip() bool {
	result, _ := p.SkipN(1)
	return result.Skipped > 0
}
func (p *GuildPlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelJobsLocked()
	p.cancelVoiceRecoveryLocked()
	p.resetProviderLocked()
	p.queue = nil
	p.loop = "off"
	p.halted, p.failures = false, 0
	p.networkAttempts = 0
	p.recoveryPaused = false
	p.retryAt = time.Time{}
	p.generation++
	p.mutationLocked("generation_changed")
	p.cancelTrackLocked("stop")
	p.mutationLocked("stop")
	p.signal("stop")
	p.persistLocked()
}

// Leave detaches first, so in-flight searches cannot resurrect playback.
// Discord orchestration serializes Leave and Attach for each guild.
func (p *GuildPlayer) Leave(ctx context.Context) {
	fields := append(p.VoiceLifecycleFields(), "component", "player", "origin", VoiceCloseOrigin(ctx), "reason", "detach_requested", "previous_state", "attached", "next_state", "detached")
	p.mu.Lock()
	v := p.voice
	if v != nil {
		p.log.Info("voice_disconnect_requested", fields...)
	}
	p.voice = nil
	p.cancelJobsLocked()
	p.cancelVoiceRecoveryLocked()
	p.resetProviderLocked()
	p.queue = nil
	p.loop = "off"
	p.halted, p.failures = false, 0
	p.networkAttempts = 0
	p.recoveryPaused = false
	p.retryAt = time.Time{}
	p.generation++
	p.mutationLocked("generation_changed")
	p.cancelTrackLocked("leave")
	p.mutationLocked("leave")
	p.signal("leave")
	p.persistLocked()
	done := p.trackDone
	p.mu.Unlock()
	if done != nil {
		<-done
	}
	if v != nil {
		v.Close(ctx)
	}
}
func (p *GuildPlayer) Close(ctx context.Context) {
	p.mu.Lock()
	if p.store != nil && p.ctx.Err() == nil {
		p.persistLocked()
	}
	p.store = nil
	p.mu.Unlock()
	p.cancel()
	if VoiceCloseOrigin(ctx) == "unattributed_close" {
		ctx = VoiceCloseContext(ctx, "player_shutdown")
	}
	p.Leave(ctx)
	<-p.done
	p.jobWorkers.Wait()
	p.persistenceWorkers.Wait()
	p.diagnostics.stopOnce.Do(func() { close(p.diagnostics.stop) })
	select {
	case <-p.diagnostics.done:
	case <-time.After(time.Second):
	}
}
func (p *GuildPlayer) run() {
	defer close(p.done)
	defer func() { p.log.Info("player_loop_exit", "cause", p.ctx.Err()) }()
	for {
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if p.voiceBlocked && p.voiceAttempts < 5 && len(p.queue) > 0 && p.voice != nil {
			p.mu.Unlock()
			p.recoverVoice()
			continue
		}
		providerPermit := ""
		if p.providerBlocked && len(p.queue) > 0 && !p.queue[0].Pending && p.voice != nil && !p.halted {
			p.mu.Unlock()
			permit, err := p.waitProvider(p.ctx, nil)
			if err != nil {
				return
			}
			p.mu.Lock()
			providerPermit = permit
		}
		reason, _ := p.schedulerReasonAllowProviderLocked(providerPermit != "")
		if reason != "ready" {
			if providerPermit != "" {
				p.finishProviderLocked(context.Canceled, providerPermit)
			}
			p.mu.Unlock()
			if !p.schedulerWait() {
				return
			}
			continue
		}
		if delay := time.Until(p.retryAt); delay > 0 {
			if providerPermit != "" {
				p.finishProviderLocked(context.Canceled, providerPermit)
			}
			p.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-p.ctx.Done():
			case <-p.wake:
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		entry := p.queue[0]
		t := entry.Track
		p.releaseEntryLocked(entry)
		p.queue[0] = queueEntry{}
		p.queue = p.queue[1:]
		ctx, cause := context.WithCancelCause(p.ctx)
		cancel := sync.OnceFunc(func() { cause(context.Canceled) })
		p.trackCause = cause
		p.sequence++
		p.diagnosticLocked(slog.LevelInfo, "scheduler_advance", "collection_id", entry.jobID, "position", t.Position, "queue_position", 1, "spotify_id", t.SpotifyID, "video_id", t.VideoID)
		sequence := p.sequence
		attemptFields := []any{"playback_attempt_id", fmt.Sprintf("%s/%d", p.sessionID, sequence), "collection_id", entry.jobID, "queue_generation", p.generation, "queue_version", p.queueVersion}
		if p.guildID != "" {
			attemptFields = append(attemptFields, "guild_id", p.guildID)
		}
		if t.Timing != nil {
			t.Timing = t.Timing.Child(attemptFields...)
			t.Timing = t.Timing.Child("sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "video_id", t.VideoID)
		} else {
			t.Timing = media.NewTiming(p.log.With(attemptFields...).With("sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "video_id", t.VideoID))
		}
		done := make(chan struct{})
		p.trackDone = done
		p.trackCancel = cancel
		p.current = &t
		p.playbackProviderID = providerPermit
		p.progress.at.Store(time.Now().UnixNano())
		p.pause = &pauseGate{}
		p.pause.set(p.recoveryPaused)
		p.recoveryPaused = false
		p.checkpointOffset = t.ResumeOffset
		p.confirmedOffset = t.ResumeOffset
		ctx = context.WithValue(ctx, pauseKey{}, p.pause)
		v := p.voice
		p.persistLocked()
		p.mu.Unlock()
		started := time.Now()
		attemptLog := p.log.With(attemptFields...)
		attemptLog.Info("next_track_selected", "sequence", sequence, "collection_id", entry.jobID, "position", t.Position, "spotify_id", t.SpotifyID, "title", t.Title, "video_id", t.VideoID)
		attemptLog.Info("track_playback_start", "sequence", sequence, "position", t.Position, "video_id", t.VideoID)
		err := callPlay(p.play, ctx, t, &startVoice{Voice: v, progress: func() { p.confirmFrame(ctx, sequence) }, start: func() { p.announceStart(ctx, t, sequence) }, metadata: func(resolved media.Track) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if ctx.Err() == nil && p.current != nil && p.sequence == sequence && p.startedSequence != sequence {
				final := copyTrack(resolved)
				final.ResumeOffset = p.current.ResumeOffset
				p.current = &final
				p.progress.at.Store(time.Now().UnixNano())
			}
		}})
		category := failureCategory(err)
		if isVoiceFailure(context.Cause(ctx)) {
			category = "voice_transport"
		} else if ctx.Err() != nil {
			category = "canceled"
		}
		attemptLog.Info("track_playback_end", "sequence", sequence, "position", t.Position, "title", t.Title, "video_id", t.VideoID, "elapsed_ms", time.Since(started).Milliseconds(), "category", category, "cancel_reason", context.Cause(ctx), "error", err)
		expectedCancel := ctx.Err() != nil
		cancel()
		p.mu.Lock()
		if category == "audio_stall" {
			p.diagnosticLocked(slog.LevelWarn, "playback_stall_detected", "component", "stream", "incident_id", fmt.Sprintf("%s/%d", p.sessionID, sequence), "duration_ms", time.Since(started).Milliseconds(), "result", "local_cancellation")
		}
		transportFailure := (isVoiceFailure(err) || isVoiceFailure(context.Cause(ctx))) && p.current != nil && p.ctx.Err() == nil
		expectedCancel = expectedCancel || p.current == nil || p.ctx.Err() != nil
		if transportFailure {
			p.suspendVoiceLocked()
			// The selected entry was released from resolver ownership. Preserve final
			// metadata as a ready entry; no rematching or fidelity downgrade.
			retained := copyTrack(*p.current)
			retained.ResumeOffset = p.confirmedOffset
			p.logInterruptedLocked(retained, "voice")
			p.recoveryPaused = p.pause.paused()
			if !advanceInterrupted(retained) {
				p.queue = append([]queueEntry{{Track: retained}}, p.queue...)
				p.mutationLocked("voice_track_retained")
			} else {
				p.mutationLocked("voice_track_completed", "confirmed_ms", retained.ResumeOffset.Milliseconds())
			}
			expectedCancel = true
		}
		providerFailure := !expectedCancel && isProviderFailure(err) && p.current != nil
		if providerFailure {
			retained := copyTrack(*p.current)
			retained.ResumeOffset = p.confirmedOffset
			if !advanceInterrupted(retained) {
				p.queue = append([]queueEntry{{Track: retained}}, p.queue...)
			}
			p.recoveryPaused = p.pause.paused()
			p.blockProviderLocked(providerFailureCategory(err))
			p.mutationLocked("provider_track_retained")
			expectedCancel = true
		}
		if providerPermit != "" {
			p.finishProviderLocked(err, providerPermit)
		}
		played := p.startedSequence == sequence
		networkRetry := !expectedCancel && err != nil && !(t.ResumeOffset > 0 && !played && category == "ffmpeg_exit") && ((played && (category == "ffmpeg_exit" || category == "yt_dlp_exit" || category == "source_http_403")) || category == "source_network" || category == "audio_stall" || category == "audio_eof" || errors.Is(err, context.DeadlineExceeded))
		if networkRetry {
			retryTrack := copyTrack(*p.current)
			retryTrack.ResumeOffset = p.confirmedOffset
			p.recoveryPaused = p.pause.paused()
			if !advanceInterrupted(retryTrack) {
				p.queue = append([]queueEntry{{Track: retryTrack}}, p.queue...)
			}
			if p.networkAttempts == 0 {
				p.networkRecoveryStarted = time.Now()
				p.networkIncident = fmt.Sprintf("%s/network/%d", p.sessionID, p.sequence)
				p.diagnosticLocked(slog.LevelWarn, "recovery_started", "component", "media", "incident_id", p.networkIncident, "cause", category)
			}
			p.logInterruptedLocked(retryTrack, "media")
			p.networkAttempts++
			p.mutationLocked("network_track_retained")
			if p.networkAttempts <= 2 {
				delay := time.Second * time.Duration(1<<(p.networkAttempts-1))
				p.retryAt = time.Now().Add(delay + time.Duration(rand.Int64N(int64(delay/4)+1)))
				p.diagnosticLocked(slog.LevelWarn, "recovery_attempted", "component", "media", "incident_id", p.networkIncident, "cause", category, "attempt", p.networkAttempts)
			} else {
				p.halted = true
				p.diagnosticLocked(slog.LevelError, "recovery_failed", "component", "media", "incident_id", p.networkIncident, "cause", category, "attempt", p.networkAttempts, "result", "queue_preserved")
			}
			expectedCancel = true
		} else if !expectedCancel && err == nil {
			p.completeRecoveryLocked()
		}
		// A failed resume must never consume the saved logical position or
		// silently restart from zero. Explicit skip/resume can resolve it.
		if err != nil && !expectedCancel && p.current != nil && t.ResumeOffset > 0 {
			saved := copyTrack(*p.current)
			saved.ResumeOffset = p.confirmedOffset
			p.queue = append([]queueEntry{{Track: saved}}, p.queue...)
			p.halted = true
			p.diagnosticLocked(slog.LevelWarn, "playback_resume_failed", "position", t.Position, "confirmed_ms", p.current.ResumeOffset.Milliseconds(), "category", category, "result", "queue_preserved")
			expectedCancel = true
		}
		terminal := Finished
		if transportFailure {
			terminal = Suspended
		} else if expectedCancel {
			terminal = Canceled
		} else if err != nil {
			terminal = Failed
		}
		attemptLog.Info("playback_attempt_terminal", "sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "state", string(terminal), "started", played)
		if !expectedCancel && err == nil && played && len(p.queue) < MaxQueueTracks {
			repeatedTrack := t
			if p.current != nil {
				repeatedTrack = *p.current
			}
			repeatedTrack.ResumeOffset = 0
			repeated := queueEntry{Track: copyTrack(repeatedTrack)}
			if p.loop == "track" {
				p.queue = append([]queueEntry{repeated}, p.queue...)
			}
			if p.loop == "queue" {
				p.queue = append(p.queue, repeated)
			}
		}
		p.lastTransition = time.Now()
		p.current = nil
		p.pause = nil
		p.trackCancel = nil
		p.trackDone = nil
		p.trackCause = nil
		if err != nil && !expectedCancel {
			p.failures++
			attemptLog.Warn("track_unavailable", "sequence", sequence, "spotify_id", t.SpotifyID, "video_id", t.VideoID, "position", t.Position, "state", "unavailable", "reason", category, "consecutive_failures", p.failures, "action", "advance_in_original_order")
		} else {
			p.failures = 0
		}
		ready, pending := p.queueCountsLocked()
		attemptLog.Info("player_transition", "sequence", sequence, "ready", ready, "pending", pending, "halted", p.halted, "provider_blocked", p.providerBlocked, "state", "transitioning", "next_pending", len(p.queue) > 0 && p.queue[0].Pending)
		if p.ctx.Err() == nil {
			// EOF must re-evaluate the resolver window too: a pending head
			// cannot commit while its collection coordinator is still asleep.
			if len(p.queue) > 0 && p.queue[0].Pending {
				p.signal("playback_end")
			}
			p.persistLocked()
		}
		close(done)
		p.mu.Unlock()
		if providerFailure {
			p.announceProvider()
		}
		if err != nil && !expectedCancel {
			stage := "starting"
			if played {
				stage = "playing"
			}
			p.announceFailure(t, err, stage, sequence)
		}
	}
}

func (p *GuildPlayer) blockProviderLocked(reason string) {
	if p.providerBlocked {
		return
	}
	p.providerBlocked = true
	if p.providerIncident == "" {
		p.providerGeneration++
		p.providerIncident = fmt.Sprintf("%s/provider/%d", p.sessionID, p.providerGeneration)
		p.providerRetries = 0
		p.providerNotice = false
	}
	p.providerRetryAt = time.Now().Add(providerBackoff(p.providerRetries))
	p.providerReason = reason
	p.signal("provider_blocked")
	p.log.Error("provider_playback_blocked", "provider", "youtube", "category", reason, "incident_id", p.providerIncident, "action", "queue retained; bounded backoff then resume")
	for _, job := range p.jobs {
		for _, cancel := range job.searches {
			if cancel != nil {
				cancel()
			}
		}
	}
}
