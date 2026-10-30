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
	mu     sync.Mutex
	resume chan struct{}
}
type pauseKey struct{}

func (g *pauseGate) set(paused bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if paused == (g.resume != nil) {
		return false
	}
	if paused {
		g.resume = make(chan struct{})
	} else {
		close(g.resume)
		g.resume = nil
	}
	return true
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
	p.providerReason = ""
	p.providerIncident = ""
	p.providerNotice = false
	p.providerProbe = false
	p.providerRetries = 0
	close(p.providerChanged)
	p.providerChanged = make(chan struct{})
}
