package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	diagnostics      *diagnostics
	queueVersion     uint64
	lastSignal       string
	sessionID        string
	loop             string
	autoplay         bool
	store            Store
	guildID          string
	persistenceError string
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	wake             chan struct{}
	log              *slog.Logger
	play             PlayFunc
	voice            Voice
	current          *media.Track
	history          []media.Track
	completedImports []CollectionProgress
	lastTransition   time.Time
	startedSequence  uint64
	queue            []queueEntry
	jobs             map[uint64]*CollectionJob
	nextJob          uint64
	jobWorkers       sync.WaitGroup
	trackCancel      context.CancelFunc
	trackDone        chan struct{}
	generation       uint64
	pause            *pauseGate
	trackCause       context.CancelCauseFunc
	onStart          func(PlaybackEvent)
	onSkip           func(PlaybackEvent)
	sequence         uint64
	halted           bool
	failures         int
	providerBlocked  bool
	providerReason   string
	providerChanged  chan struct{}
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
	p.resetProviderLocked()
	p.queue = nil
	p.loop = "off"
	p.halted, p.failures = false, 0
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
	p.mu.Lock()
	v := p.voice
	p.voice = nil
	p.cancelJobsLocked()
	p.resetProviderLocked()
	p.queue = nil
	p.loop = "off"
	p.halted, p.failures = false, 0
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
	p.Leave(ctx)
	<-p.done
	p.jobWorkers.Wait()
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
		if reason, _ := p.schedulerReasonLocked(); reason != "ready" {
			p.mu.Unlock()
			if !p.schedulerWait() {
				return
			}
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
		p.pause = &pauseGate{}
		ctx = context.WithValue(ctx, pauseKey{}, p.pause)
		v := p.voice
		p.persistLocked()
		p.mu.Unlock()
		started := time.Now()
		attemptLog := p.log.With(attemptFields...)
		attemptLog.Info("next_track_selected", "sequence", sequence, "collection_id", entry.jobID, "position", t.Position, "spotify_id", t.SpotifyID, "title", t.Title, "video_id", t.VideoID)
		attemptLog.Info("track_playback_start", "sequence", sequence, "position", t.Position, "video_id", t.VideoID)
		err := callPlay(p.play, ctx, t, &startVoice{Voice: v, start: func() { p.announceStart(ctx, t, sequence) }, metadata: func(resolved media.Track) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if ctx.Err() == nil && p.current != nil && p.sequence == sequence && p.startedSequence != sequence {
				final := copyTrack(resolved)
				p.current = &final
			}
		}})
		category := failureCategory(err)
		if ctx.Err() != nil {
			category = "canceled"
		}
		attemptLog.Info("track_playback_end", "sequence", sequence, "position", t.Position, "title", t.Title, "video_id", t.VideoID, "elapsed_ms", time.Since(started).Milliseconds(), "category", category, "cancel_reason", context.Cause(ctx), "error", err)
		expectedCancel := ctx.Err() != nil
		cancel()
		p.mu.Lock()
		expectedCancel = expectedCancel || p.current == nil || p.ctx.Err() != nil
		played := p.startedSequence == sequence
		terminal := Finished
		if expectedCancel {
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
	p.providerReason = reason
	p.log.Error("provider_playback_blocked", "provider", "youtube", "category", reason, "action", "queue retained; use resume to retry the same position")
	for _, job := range p.jobs {
		if job.blockedPosition < 0 {
			for _, entry := range p.queue {
				if entry.jobID == job.progress.ID && entry.Pending {
					job.blockedPosition = entry.position
					break
				}
			}
		}
		for _, cancel := range job.searches {
			if cancel != nil {
				cancel()
			}
		}
	}
}

func (p *GuildPlayer) waitProvider(ctx context.Context) error {
	for {
		p.mu.Lock()
		blocked, changed := p.providerBlocked, p.providerChanged
		p.mu.Unlock()
		if !blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (p *GuildPlayer) blockProviderAt(job *CollectionJob, position int, category string) {
	p.mu.Lock()
	if job != nil && job.blockedPosition < 0 {
		job.blockedPosition = position
	}
	p.blockProviderLocked(category)
	p.signal("provider_blocked")
	p.mu.Unlock()
}
