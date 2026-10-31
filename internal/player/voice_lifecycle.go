package player

import (
	"context"
	"fmt"
)

type voiceCloseKey struct{}

// VoiceCloseContext carries a logical origin through transport adapters. It
// contains no Discord session credentials and never changes cancellation.
func VoiceCloseContext(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, voiceCloseKey{}, origin)
}
func VoiceCloseOrigin(ctx context.Context) string {
	if origin, ok := ctx.Value(voiceCloseKey{}).(string); ok {
		return origin
	}
	return "unattributed_close"
}

// VoiceLifecycleFields snapshots confirmed outbound progress, not wall time.
// Callers must not hold the player mutex.
func (p *GuildPlayer) VoiceLifecycleFields() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	incident := p.voiceIncidentID
	if !p.voiceIncident || incident == "" {
		incident = fmt.Sprintf("%s/voice/%d", p.sessionID, p.sequence)
	}
	fields := []any{"incident_id", incident, "guild_id", p.guildID, "sequence", p.sequence, "confirmed_ms", p.confirmedOffset.Milliseconds(), "queue_size", len(p.queue), "voice_suspended", p.voiceBlocked, "paused", p.pause.paused()}
	if p.current != nil {
		fields = append(fields, "position", p.current.Position, "title", p.current.Title, "duration_ms", p.current.Duration.Milliseconds())
	}
	return fields
}
