package player

import (
	"context"
	"errors"
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
	go p.run()
	return p
}
func (p *GuildPlayer) signal() {
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
	p.signal()
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
	p.halted, p.failures = false, 0
	p.generation++
	p.cancelTrackLocked("stop")
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
	p.halted, p.failures = false, 0
	p.generation++
	p.cancelTrackLocked("leave")
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
		if p.voice == nil || p.halted || p.providerBlocked || len(p.queue) == 0 || p.queue[0].Pending {
			p.mu.Unlock()
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
				continue
			}
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
		sequence := p.sequence
		if t.Timing != nil {
			t.Timing = t.Timing.Child("sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "video_id", t.VideoID)
		} else {
			t.Timing = media.NewTiming(p.log.With("sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "video_id", t.VideoID))
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
		p.log.Info("next_track_selected", "sequence", sequence, "collection_id", entry.jobID, "position", t.Position, "spotify_id", t.SpotifyID, "title", t.Title, "video_id", t.VideoID)
		p.log.Info("track_playback_start", "sequence", sequence, "position", t.Position, "video_id", t.VideoID)
		err := callPlay(p.play, ctx, t, &startVoice{Voice: v, start: func() { p.announceStart(ctx, t, sequence) }})
		category := failureCategory(err)
		if ctx.Err() != nil {
			category = "canceled"
		}
		p.log.Info("track_playback_end", "sequence", sequence, "position", t.Position, "title", t.Title, "video_id", t.VideoID, "elapsed_ms", time.Since(started).Milliseconds(), "category", category, "cancel_reason", context.Cause(ctx), "error", err)
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
		p.log.Info("playback_attempt_terminal", "sequence", sequence, "spotify_id", t.SpotifyID, "position", t.Position, "state", terminal, "started", played)
		p.lastTransition = time.Now()
		p.current = nil
		p.pause = nil
		p.trackCancel = nil
		p.trackDone = nil
		p.trackCause = nil
		if err != nil && !expectedCancel {
			p.failures++
			p.log.Warn("track_unavailable", "sequence", sequence, "spotify_id", t.SpotifyID, "video_id", t.VideoID, "position", t.Position, "state", "unavailable", "reason", category, "consecutive_failures", p.failures, "action", "advance_in_original_order")
		} else {
			p.failures = 0
		}
		ready, pending := p.queueCountsLocked()
		p.log.Info("player_transition", "sequence", sequence, "ready", ready, "pending", pending, "halted", p.halted, "provider_blocked", p.providerBlocked, "state", "transitioning", "next_pending", len(p.queue) > 0 && p.queue[0].Pending)
		if p.ctx.Err() == nil {
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
	p.mu.Unlock()
	p.signal()
}
