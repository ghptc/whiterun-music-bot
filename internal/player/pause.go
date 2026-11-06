package player

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Each track owns a gate. Closing the resume channel wakes all waiters;
// cancellation never depends on a resume or on the player's mutex.
type pauseGate struct {
	mu            sync.Mutex
	resume        chan struct{}
	changed       chan struct{}
	sourceManaged bool
	sourceResume  chan struct{}
}
type pauseKey struct{}

func (g *pauseGate) set(paused bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if paused == (g.resume != nil) {
		return false
	}
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
	if paused {
		g.resume = make(chan struct{})
	} else {
		close(g.resume)
		g.resume = nil
	}
	return true
}
func (g *pauseGate) snapshot() (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	return g.resume != nil, g.changed
}

// sourcePause hands native source control responsibility to the adapter. Keep
// draining audio until its Pause acknowledgement so the daemon cannot deadlock
// waiting for a FIFO write while downstream delivery is already paused.
func (g *pauseGate) sourcePause() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sourceManaged = true
	if g.resume != nil && g.sourceResume == nil {
		g.sourceResume = make(chan struct{})
	}
	return g.resume != nil
}
func (g *pauseGate) sourceAcknowledged(paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if paused && g.sourceResume == nil {
		g.sourceResume = make(chan struct{})
	}
	if !paused && g.sourceResume != nil {
		close(g.sourceResume)
		g.sourceResume = nil
	}
}
func (g *pauseGate) paused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.resume != nil
}
func waitPlayback(ctx context.Context) (bool, error) {
	g, _ := ctx.Value(pauseKey{}).(*pauseGate)
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			return waited, err
		}
		if g == nil {
			return waited, nil
		}
		g.mu.Lock()
		resume := g.resume
		if g.sourceManaged {
			resume = g.sourceResume
		}
		g.mu.Unlock()
		if resume == nil {
			return waited, nil
		}
		waited = true
		select {
		case <-ctx.Done():
			return waited, ctx.Err()
		case <-resume:
		}
	}
}

// SetPaused changes only the active track; the next track always starts playing.
func (p *GuildPlayer) SetPaused(paused bool) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.voiceBlocked && len(p.queue) > 0 {
		p.recoveryPaused = paused
		if paused {
			return true, nil
		}
		if p.voiceAttempts >= 5 {
			p.voiceAttempts = 0
		}
		p.signal("voice_recovery_requested")
		return true, nil
	}
	if !paused && p.providerBlocked {
		if p.providerGlobal && p.admission != nil {
			// A healthy stream paused by its owner is independent of pending
			// provider recovery; resuming it must not renew the global budget.
			if p.current != nil && p.pause != nil && p.pause.paused() {
				changed := p.pause.set(false)
				p.wakeCollectionsLocked()
				p.diagnosticLocked(slog.LevelInfo, "playback_pause_changed", "paused", false, "expected_event", "resume_or_cancel")
				return changed, nil
			}
			if p.admission.Snapshot().Exhausted {
				return false, errors.New("YouTube recovery budget is exhausted globally; operator rearm is required after access is restored.")
			}
			p.signal("provider_recovery_requested")
			return true, nil
		}
		reason := p.providerReason
		if p.providerProbe {
			return true, nil
		}
		if len(p.queue) == 0 && len(p.jobs) == 0 {
			p.resetProviderLocked()
			p.signal("provider_retry_new_request")
			return true, nil
		}
		p.providerRetries = 0
		p.providerRetryAt = time.Now()
		close(p.providerChanged)
		p.providerChanged = make(chan struct{})
		p.failures = 0
		p.log.Info("provider_playback_recovery_requested", "provider", "youtube", "previous_category", reason, "action", "retry same logical position")
		p.signal("resume")
		return true, nil
	}
	if !paused && p.halted {
		p.halted = false
		p.networkAttempts = 0
		p.retryAt = time.Time{}
		p.failures = 0
		p.signal("resume")
		return true, nil
	}
	if p.current == nil || p.pause == nil || p.ctx.Err() != nil {
		return false, errors.New("The Bard is not playing.")
	}
	changed := p.pause.set(paused)
	if changed {
		p.wakeCollectionsLocked()
		p.diagnosticLocked(slog.LevelInfo, "playback_pause_changed", "paused", paused, "expected_event", "resume_or_cancel")
	}
	return changed, nil
}

// A stopped or detached session must not carry a provider latch into a fresh one.
func (p *GuildPlayer) resetProviderLocked() {
	if !p.providerBlocked && p.providerIncident == "" {
		return
	}
	p.providerBlocked = false
	p.providerGlobal = false
	p.providerReason = ""
	p.providerIncident = ""
	p.providerNotice = false
	p.providerProbe = false
	p.providerRetries = 0
	close(p.providerChanged)
	p.providerChanged = make(chan struct{})
}
