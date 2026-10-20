package player

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
	if !paused && p.providerBlocked {
		reason := p.providerReason
		p.providerBlocked = false
		p.providerReason = ""
		close(p.providerChanged)
		p.providerChanged = make(chan struct{})
		p.failures = 0
		p.log.Info("provider_playback_recovery_requested", "provider", "youtube", "previous_category", reason, "action", "retry same logical position")
		p.signal("resume")
		return true, nil
	}
	if !paused && p.halted {
		p.halted = false
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
	if !p.providerBlocked {
		return
	}
	p.providerBlocked = false
	p.providerReason = ""
	close(p.providerChanged)
	p.providerChanged = make(chan struct{})
}
