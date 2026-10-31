package player

import (
	"context"
	"errors"
	"time"
	"whiterun/internal/media"
)

const providerAutomaticRetries = media.ProviderRecoveryTrials

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
	p.observeProviderLocked(err)
	p.blockProviderLocked(providerFailureCategory(err))
	p.mu.Unlock()
	p.announceProvider()
}

func providerBackoff(attempt int) time.Duration {
	return media.ProviderBackoff(attempt)
}

// Every resolver and ready playback attempt enters here. During an incident
// exactly one caller receives a trial; other callers wait for its result.
// Global exhaustion requires operator rearm; guild controls never renew its budget.
func (p *GuildPlayer) waitProvider(ctx context.Context, suspended func()) (string, error) {
	if p.admission != nil {
		shared := p.admission.Snapshot()
		p.mu.Lock()
		global := shared.Blocked || p.providerGlobal
		if shared.Blocked {
			p.blockProviderLocked(shared.Category)
		}
		p.mu.Unlock()
		if global {
			p.announceProvider()
			if suspended != nil {
				suspended()
			}
			permit, err := p.admission.WaitRecovery(ctx)
			p.mu.Lock()
			if err == nil {
				if permit == "" {
					p.resetProviderLocked()
				} else {
					status := p.admission.Snapshot()
					p.providerProbe = true
					p.providerRetries = status.Attempts
					p.log.Info("provider_recovery_attempted", "incident_id", status.Incident, "attempt", status.Attempts, "scope", "process")
				}
			}
			p.mu.Unlock()
			return permit, err
		}
	}
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
	if p.providerGlobal && p.admission != nil {
		p.admission.FinishTrial(incident, err)
		shared := p.admission.Snapshot()
		p.providerProbe = false
		p.providerRetries = shared.Attempts
		if !shared.Blocked {
			p.resetProviderLocked()
			p.signal("provider_recovered")
		} else {
			p.providerRetryAt = shared.RetryAt
			p.signal("provider_backoff")
		}
		return
	}
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
	if p.providerNotice || p.providerIncident == "" || p.providerLastNotice == p.providerIncident {
		p.mu.Unlock()
		return
	}
	p.providerNotice = true
	p.providerLastNotice = p.providerIncident
	reason := "YouTube access is temporarily unavailable. The queue and confirmed progress are preserved. Recovery uses bounded backoff; use resume after provider access is restored."
	if p.providerGlobal {
		reason = "YouTube access is temporarily unavailable. The queue and confirmed progress are preserved. Recovery is shared across guilds with bounded backoff; exhausted recovery requires operator intervention."
	}
	event := PlaybackEvent{IncidentID: p.providerIncident, State: Suspended, FailureStage: "provider", SkippedReason: reason}
	fn := p.onSkip
	p.mu.Unlock()
	if fn != nil {
		fn(event)
	}
}

// ConfigureYouTube is startup-only; a shared gate replaces per-guild trials for
// confirmed provider incidents. Legacy/local failures retain their own policy.
func (p *GuildPlayer) ConfigureYouTube(admission *media.ProviderAdmission, horizon time.Duration) error {
	if horizon < 0 || horizon > media.VerificationPrefetchHorizon {
		return errors.New("invalid playback horizon")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil || p.voice != nil {
		return errors.New("configure YouTube before attaching Voice")
	}
	p.admission = admission
	p.prefetchHorizon = horizon
	return nil
}
func (p *GuildPlayer) observeProviderLocked(err error) {
	if p.admission != nil && !p.admission.Snapshot().Blocked {
		p.admission.Observe(err)
	}
}
