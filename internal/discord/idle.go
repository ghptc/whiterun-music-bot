package discord

import (
	"context"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

const emptyChannelGrace = 2 * time.Minute

// Leave paths share the same serialized session reset. Caller holds g.mu.
func (g *guild) leave(ctx, parent context.Context) {
	g.emptySince = time.Time{}
	g.unhealthySince = time.Time{}
	g.announcementMu.Lock()
	g.announcementChannel.Store(0)
	g.announcementEpoch.Add(1)
	g.announcementMu.Unlock()
	g.resetSearches(parent)
	g.player.Leave(ctx)
	g.conn = nil
	g.channel = 0
}

// Unknown members conservatively count as listeners. Voice-state payloads and
// the initial guild payload populate the member cache without privileged intents.
func (b *Bot) hasListeners(id snowflake.ID, g *guild) bool {
	for state := range b.client.Caches.VoiceStates(id) {
		if state.UserID == b.client.ID() || state.ChannelID == nil || *state.ChannelID != g.channel {
			continue
		}
		member, known := b.client.Caches.Member(id, state.UserID)
		if !known || !member.User.Bot {
			return true
		}
	}
	return false
}

// The existing watchVoice worker owns deadline checks: no timer goroutine per
// guild, and no callback can outlive shutdown or act on an old voice session.
func (b *Bot) checkEmpty(id snowflake.ID, g *guild, now time.Time) {
	if g.conn == nil || b.ctx.Err() != nil || b.hasListeners(id, g) {
		g.emptySince = time.Time{}
		return
	}
	if g.emptySince.IsZero() {
		g.emptySince = now
		return
	}
	if now.Sub(g.emptySince) < emptyChannelGrace {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.leave(ctx, b.ctx)
	b.log.Info("empty voice channel; cleared playback and disconnected", "guild_id", id, "stage", "voice_idle")
}

func (b *Bot) voiceStateChanged(e *events.GuildVoiceStateUpdate) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return
	}
	// DisGo's voice event member may omit GuildID; scope it explicitly before
	// caching so bot classification also works for users joining after startup.
	member := e.Member
	if member.User.ID != 0 {
		member.GuildID = e.VoiceState.GuildID
		b.client.Caches.AddMember(member)
	}
	g := b.guilds[e.VoiceState.GuildID]
	if g == nil {
		b.mu.Unlock()
		return
	}
	b.workers.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.workers.Done()
		g.mu.Lock()
		// Reset even for a brief human visit between watch iterations. Read the
		// cache during checks so late event delivery cannot evict a current listener.
		if !e.Member.User.Bot && e.VoiceState.ChannelID != nil && *e.VoiceState.ChannelID == g.channel {
			g.emptySince = time.Time{}
		}
		g.mu.Unlock()
		select {
		case b.voiceWake <- struct{}{}:
		default:
		}
	}()
}

const voiceRecoveryGrace = 60 * time.Second

// A reconnecting gateway is not proof of a kick. Cache absence while the main
// gateway reconnects is also not authoritative. Only a known move/leave with a
// ready main gateway is immediately terminal; other unhealthy states get grace.
// Caller holds g.mu. True means skip idle checks against stale caches.
func (b *Bot) checkVoiceHealth(id snowflake.ID, g *guild, now time.Time, mainReady, voiceReady, known, sameChannel bool) bool {
	if mainReady && voiceReady && known && sameChannel {
		if !g.unhealthySince.IsZero() {
			b.log.Info("voice_recovered", "guild_id", id, "elapsed_ms", now.Sub(g.unhealthySince).Milliseconds())
		}
		g.unhealthySince = time.Time{}
		return false
	}
	reason := "gateway_reconnecting"
	terminal := mainReady && known && !sameChannel
	if terminal {
		reason = "voice_moved_or_removed"
	}
	if g.unhealthySince.IsZero() {
		g.unhealthySince = now
		b.log.Warn("voice_unhealthy", "guild_id", id, "main_ready", mainReady, "voice_ready", voiceReady, "cache_known", known, "same_channel", sameChannel, "reason", reason)
	}
	if !terminal && now.Sub(g.unhealthySince) < voiceRecoveryGrace {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.leave(ctx, b.ctx)
	b.log.Warn("voice_disconnected", "guild_id", id, "reason", reason, "main_ready", mainReady, "voice_ready", voiceReady, "cache_known", known, "action", "cleared playback")
	return true
}
