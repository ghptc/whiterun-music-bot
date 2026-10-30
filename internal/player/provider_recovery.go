package player

import (
	"context"
	"errors"
	"time"
)

const providerAutomaticRetries = 3

// Preparation of a direct /play can fail before an occurrence is reserved.
// Share that provider incident with the player without changing current audio.
func (p *GuildPlayer) SuspendProvider(err error) {
	if !isProviderFailure(err) {
		return
	}
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.blockProviderLocked(providerFailureCategory(err))
	p.mu.Unlock()
	p.announceProvider()
}

func providerBackoff(attempt int) time.Duration {
	return min(2*time.Minute, 15*time.Second*time.Duration(1<<min(attempt, 3)))
}

// Every resolver and ready playback attempt enters here. During an incident
// exactly one caller receives a trial; other callers wait for its result.
// After three unsuccessful trials only an explicit resume opens another budget.
func (p *GuildPlayer) waitProvider(ctx context.Context, suspended func()) (string, error) {
	for {
		p.mu.Lock()
		if ctx.Err() != nil {
			p.mu.Unlock()
			return "", ctx.Err()
		}
		if !p.providerBlocked {
			p.mu.Unlock()
			return "", nil
		}
		changed := p.providerChanged
		delay := time.Until(p.providerRetryAt)
		canProbe := !p.providerProbe && p.providerRetries < providerAutomaticRetries
		if canProbe && delay <= 0 {
			p.providerProbe = true
			p.providerRetries++
			p.log.Info("provider_recovery_attempted", "incident_id", p.providerIncident, "attempt", p.providerRetries)
			incident := p.providerIncident
			p.mu.Unlock()
			if suspended != nil {
				suspended()
			}
			return incident, nil
		}
		p.mu.Unlock()
		if suspended != nil {
			suspended()
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if canProbe {
			timer = time.NewTimer(max(time.Millisecond, delay))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return "", ctx.Err()
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (p *GuildPlayer) finishProviderLocked(err error, incident string) {
	if !p.providerProbe || incident != p.providerIncident {
		return
	}
	p.providerProbe = false
	if err == nil || (!isProviderFailure(err) && !errors.Is(err, context.Canceled)) {
		p.log.Info("provider_recovery_completed", "incident_id", p.providerIncident)
		p.resetProviderLocked()
		p.signal("provider_recovered")
	} else {
		if errors.Is(err, context.Canceled) && p.providerRetries > 0 {
			p.providerRetries--
		}
		p.providerRetryAt = time.Now().Add(providerBackoff(p.providerRetries))
		if p.providerRetries >= providerAutomaticRetries {
			p.log.Warn("provider_recovery_exhausted", "incident_id", p.providerIncident, "attempts", p.providerRetries, "action", "queue retained; explicit resume required")
		}
		close(p.providerChanged)
		p.providerChanged = make(chan struct{})
		p.signal("provider_backoff")
	}
}

func (p *GuildPlayer) announceProvider() {
	p.mu.Lock()
	if p.providerNotice || p.providerIncident == "" {
		p.mu.Unlock()
		return
	}
	p.providerNotice = true
	event := PlaybackEvent{IncidentID: p.providerIncident, State: Suspended, FailureStage: "provider", SkippedReason: "YouTube access is temporarily unavailable. The queue and confirmed progress are preserved. Recovery uses bounded backoff; use resume after provider access is restored."}
	fn := p.onSkip
	p.mu.Unlock()
	if fn != nil {
		fn(event)
	}
}
