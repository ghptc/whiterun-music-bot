package player

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// RecoverableVoice creates a fresh, fully negotiated transport. It must never
// return a connection with an invalidated encryption session.
type RecoverableVoice interface {
	Recover(context.Context) (Voice, error)
}

var errVoiceUnavailable = errors.New("voice transport unavailable")

// Inspect the entire joined error tree: process cleanup may add a media error
// before the original transport error.
func isVoiceFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errVoiceUnavailable) {
		return true
	}
	if e, ok := err.(*StreamError); ok {
		switch e.Category {
		case "voice_speaking", "voice_send", "voice_transport":
			return true
		}
	}
	if e, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range e.Unwrap() {
			if isVoiceFailure(child) {
				return true
			}
		}
		return false
	}
	return isVoiceFailure(errors.Unwrap(err))
}

// SuspendVoice interrupts audio, not the logical track. run owns all teardown
// and reconnect attempts; repeated health checks cannot create parallel recovery.
func (p *GuildPlayer) SuspendVoice() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.voice == nil || p.ctx.Err() != nil {
		return
	}
	p.suspendVoiceLocked()
	if p.trackCause != nil && p.current != nil {
		p.trackCause(errVoiceUnavailable)
	}
	p.signal("voice_suspended")
}
func (p *GuildPlayer) suspendVoiceLocked() {
	if !p.voiceBlocked {
		p.voiceBlocked = true
		if !p.voiceIncident {
			p.voiceIncident = true
			p.voiceAttempts = 0
			p.voiceNotice = false
		}
		p.log.Warn("voice_circuit_open", "guild_id", p.guildID, "action", "preserve track and queue")
	}
}

// Called by the sole scheduler outside mu. Retries have a finite budget per
// incident, including across skip/enqueue. Exhaustion retains the queue.
func (p *GuildPlayer) recoverVoice() {
	p.mu.Lock()
	if !p.voiceBlocked || p.voiceAttempts >= 5 || len(p.queue) == 0 || p.voice == nil {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.voiceRecoveryCancel = cancel
	epoch := p.voiceEpoch
	v := p.voice
	notice := !p.voiceNotice
	p.voiceNotice = true
	incident := fmt.Sprintf("%s/voice/%d", p.sessionID, p.sequence)
	started := time.Now()
	fn := p.onSkip
	p.mu.Unlock()
	defer cancel()
	p.log.Warn("recovery_started", "incident_id", incident, "component", "voice", "cause", "transport_unavailable")
	if notice && fn != nil {
		fn(PlaybackEvent{FailureStage: "voice", SkippedReason: "Discord Voice interrupted. The track and queue are preserved while the Bard reconnects.", State: Suspended})
	}
	for {
		p.mu.Lock()
		if ctx.Err() != nil || epoch != p.voiceEpoch || p.voiceAttempts >= 5 {
			p.mu.Unlock()
			return
		}
		attempt := p.voiceAttempts
		p.voiceAttempts++
		p.mu.Unlock()
		delay := time.Second * time.Duration(1<<attempt)
		delay += time.Duration(rand.Int64N(int64(delay/4) + 1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var replacement Voice
		var err error
		p.log.Info("recovery_attempted", "incident_id", incident, "component", "voice", "attempt", attempt+1, "duration_ms", time.Since(started).Milliseconds())
		attemptCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		if r, ok := v.(RecoverableVoice); ok {
			replacement, err = r.Recover(attemptCtx)
		} else {
			err = errors.New("voice transport has no recovery implementation")
		}
		stop()
		p.mu.Lock()
		if ctx.Err() != nil || epoch != p.voiceEpoch {
			p.mu.Unlock()
			return
		}
		if err == nil && replacement != nil {
			p.voice = replacement
			p.voiceBlocked = false
			p.voiceRecoveryCancel = nil
			p.signal("voice_recovered")
			p.log.Info("recovery_completed", "incident_id", incident, "component", "voice", "guild_id", p.guildID, "attempt", attempt+1, "duration_ms", time.Since(started).Milliseconds())
			p.mu.Unlock()
			// The actual first frame supplies the single relevant recovery update.
			return
		}
		p.log.Warn("voice_recovery_failed", "guild_id", p.guildID, "attempt", attempt+1, "error", err)
		exhausted := p.voiceAttempts >= 5
		p.mu.Unlock()
		if exhausted {
			p.log.Error("recovery_failed", "incident_id", incident, "component", "voice", "duration_ms", time.Since(started).Milliseconds(), "guild_id", p.guildID, "action", "queue preserved; no further automatic streaming")
			return
		}
	}
}

func (p *GuildPlayer) cancelVoiceRecoveryLocked() {
	p.voiceEpoch++
	if p.voiceRecoveryCancel != nil {
		p.voiceRecoveryCancel()
		p.voiceRecoveryCancel = nil
	}
}

// IsVoiceFailure lets the media boundary avoid invalidating a valid locator
// when Discord transport, rather than the recording, failed.
func IsVoiceFailure(err error) bool { return isVoiceFailure(err) }
