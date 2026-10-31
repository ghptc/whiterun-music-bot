package player

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"whiterun/internal/media"
)

// Opus output is fixed at 48 kHz, 960 samples (20 ms) per audio packet.
// A successful send confirms outbound audio only, never receipt or listening.
func (p *GuildPlayer) confirmFrame(ctx context.Context, sequence uint64) {
	p.progress.at.Store(time.Now().UnixNano())
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil || p.current == nil || p.sequence != sequence {
		return
	}
	p.confirmedOffset += 20 * time.Millisecond
	if p.confirmedOffset-p.current.ResumeOffset >= 5*time.Second {
		if p.playbackProviderID != "" {
			p.finishProviderLocked(nil, p.playbackProviderID)
			p.playbackProviderID = ""
		}
		p.completeRecoveryLocked()
	}
	if p.confirmedOffset-p.checkpointOffset >= 5*time.Second {
		p.checkpointOffset = p.confirmedOffset
		p.wakeCollectionsLocked()
		p.persistAudioCheckpointLocked()
	}
}

func advanceInterrupted(t media.Track) bool {
	return !t.Live && t.Duration > 0 && t.ResumeOffset >= (t.Duration+1)/2
}

func (p *GuildPlayer) logInterruptedLocked(t media.Track, component string) {
	incident := p.voiceIncidentID
	if component != "voice" {
		incident = p.networkIncident
	}
	if incident == "" {
		incident = fmt.Sprintf("%s/%s/%d", p.sessionID, component, p.sequence)
	}
	p.diagnosticLocked(slog.LevelWarn, "audio_stream_interrupted", "incident_id", incident, "component", component, "position", t.Position, "confirmed_ms", t.ResumeOffset.Milliseconds(), "duration_ms", t.Duration.Milliseconds(), "advance", advanceInterrupted(t))
}

// One accepted frame is not enough to reset a failing transport's retry budget.
func (p *GuildPlayer) completeRecoveryLocked() {
	p.networkAttempts = 0
	if p.voiceIncident {
		p.voiceIncident = false
		p.voiceAttempts = 0
		p.voiceNotice = false
	}
}

// Keep a busy disk off the audio path beyond a small bounded budget. A failed
// periodic checkpoint is retried at the next audio interval; command writes
// retain their existing durable retry policy. mu serializes both kinds, so an
// older snapshot can never overwrite a newer stop/skip transaction.
func (p *GuildPlayer) persistAudioCheckpointLocked() {
	if p.store == nil || p.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 100*time.Millisecond)
	defer cancel()
	if err := p.store.SaveGuild(ctx, p.guildID, p.durableLocked()); err != nil {
		if !p.checkpointDelayed {
			p.diagnosticLocked(slog.LevelWarn, "playback_checkpoint_delayed", "reason", "storage_write_failed", "confirmed_ms", p.confirmedOffset.Milliseconds())
		}
		p.checkpointDelayed = true
		if p.persistenceError == "" {
			p.persistenceError = "Playback checkpoint delayed; crash recovery may repeat additional audio."
		}
	} else {
		p.checkpointDelayed = false
		if p.persistenceError == "Playback checkpoint delayed; crash recovery may repeat additional audio." {
			p.persistenceError = ""
		}
	}
}
