package player

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const schedulerStallAfter = 30 * time.Second

// Diagnostics never invoke a handler while holding the player lock. Admission
// is bounded and lossy; timestamps describe the decision, not the writer flush.
type diagnostics struct {
	events   chan slog.Record
	stop     chan struct{}
	done     chan struct{}
	dropped  atomic.Uint64
	stopOnce sync.Once
}

func newDiagnostics(log *slog.Logger) *diagnostics {
	d := &diagnostics{events: make(chan slog.Record, 256), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		write := func(r slog.Record) {
			if log != nil && log.Enabled(context.Background(), r.Level) {
				_ = log.Handler().Handle(context.Background(), r)
			}
		}
		for {
			select {
			case r := <-d.events:
				write(r)
			case <-d.stop:
				for {
					select {
					case r := <-d.events:
						write(r)
					default:
						return
					}
				}
			}
		}
	}()
	return d
}
func (p *GuildPlayer) diagnosticLocked(level slog.Level, event string, fields ...any) {
	if p.diagnostics == nil {
		return
	}
	r := slog.NewRecord(time.Now(), level, event, 0)
	if p.guildID != "" {
		r.Add("guild_id", p.guildID)
	}
	r.Add("queue_generation", p.generation, "queue_version", p.queueVersion, "sequence", p.sequence)
	if event == "scheduler_advance" {
		r.Add("playback_attempt_id", fmt.Sprintf("%s/%d", p.sessionID, p.sequence))
	}
	r.Add(fields...)
	r.Add("diagnostic_dropped_total", p.diagnostics.dropped.Load())
	select {
	case p.diagnostics.events <- r:
	default:
		p.diagnostics.dropped.Add(1)
	}
}
func (p *GuildPlayer) mutationLocked(action string, fields ...any) {
	p.queueVersion++
	p.diagnosticLocked(slog.LevelInfo, "queue_mutation", append([]any{"action", action, "queue_size", len(p.queue)}, fields...)...)
}
func (p *GuildPlayer) schedulerReasonLocked() (string, string) {
	switch {
	case p.ctx.Err() != nil:
		return "canceled", "none"
	case p.voice == nil:
		return "voice_detached", "voice_attach"
	case p.voiceBlocked:
		return "voice_suspended", "voice_recovery"
	case p.providerBlocked:
		return "provider_blocked", "provider_recovery"
	case p.halted:
		return "halted", "resume"
	case len(p.queue) == 0:
		return "queue_empty", "queue_insert"
	case p.queue[0].Pending:
		e := p.queue[0]
		if j := p.jobs[e.jobID]; j != nil && j.searches[e.position] != nil {
			return "resolution_in_progress", "resolution_commit"
		}
		return "next_position_pending", "resolver_window_wakeup_then_commit"
	default:
		return "ready", "scheduler_advance"
	}
}
func (p *GuildPlayer) schedulerEventLocked(level slog.Level, event, reason, expected string, elapsed time.Duration) {
	ready, pending := p.queueCountsLocked()
	active := 0
	for _, j := range p.jobs {
		for _, search := range j.searches {
			if search != nil {
				active++
			}
		}
	}
	fields := []any{"wait_reason", reason, "expected_event", expected, "elapsed_wait_ms", elapsed.Milliseconds(), "ready_count", ready, "pending_count", pending, "active_resolutions", active, "provider_blocked", p.providerBlocked, "halted", p.halted, "last_relevant_event", p.lastSignal}
	state := "waiting"
	if len(p.queue) > 0 && !p.lastTransition.IsZero() {
		state = "transitioning"
	}
	fields = append(fields, "player_state", state)
	if len(p.queue) > 0 {
		e := p.queue[0]
		fields = append(fields, "collection_id", e.jobID, "next_expected_position", e.Track.Position, "queue_position", 1, "spotify_id", e.SpotifyID, "video_id", e.VideoID, "next_position_pending", e.Pending)
	}
	if event == "scheduler_stall_detected" {
		fields = append(fields, "goroutines", runtime.NumGoroutine())
	}
	p.diagnosticLocked(level, event, fields...)
}

// One timer per wait, one warning per unchanged blocking condition. It observes
// only: it never cancels, retries or advances playback.
func (p *GuildPlayer) schedulerWait() bool {
	started := time.Now()
	timer := time.NewTimer(schedulerStallAfter)
	defer timer.Stop()
	lastReason := ""
	for {
		p.mu.Lock()
		reason, expected := p.schedulerReasonLocked()
		if reason == "ready" {
			p.schedulerEventLocked(slog.LevelInfo, "scheduler_track_ready", reason, expected, time.Since(started))
			p.mu.Unlock()
			return true
		}
		if reason != lastReason {
			started = time.Now()
			timer.Reset(schedulerStallAfter)
			p.schedulerEventLocked(slog.LevelInfo, "scheduler_wait", reason, expected, time.Since(started))
			lastReason = reason
		}
		p.mu.Unlock()
		for waiting := true; waiting; {
			select {
			case <-p.ctx.Done():
				p.mu.Lock()
				p.diagnosticLocked(slog.LevelInfo, "scheduler_wakeup", "wakeup_reason", "canceled", "elapsed_wait_ms", time.Since(started).Milliseconds())
				p.mu.Unlock()
				return false
			case <-p.wake:
				waiting = false
				p.mu.Lock()
				p.diagnosticLocked(slog.LevelInfo, "scheduler_wakeup", "wakeup_reason", p.lastSignal, "elapsed_wait_ms", time.Since(started).Milliseconds())
				p.mu.Unlock()
			case <-timer.C:
				p.mu.Lock()
				reason, expected = p.schedulerReasonLocked()
				if reason == lastReason && (reason == "next_position_pending" || reason == "resolution_in_progress") {
					p.schedulerEventLocked(slog.LevelWarn, "scheduler_stall_detected", reason, expected, time.Since(started))
				}
				if reason == "ready" {
					p.schedulerEventLocked(slog.LevelWarn, "scheduler_stall_detected", "notification_missing", "scheduler_notification", time.Since(started))
				}
				p.mu.Unlock()
			}
		}
	}
}
