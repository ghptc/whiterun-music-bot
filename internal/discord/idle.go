package discord

import (
	"context"
	"time"
	"whiterun/internal/player"

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
	if g.conn != nil {
		g.conn.Close(ctx)
	}
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
	g.leave(player.VoiceCloseContext(ctx, "empty_channel_grace_expired"), b.ctx)
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

// Voice infrastructure loss must preserve the queue, including known moves
// and disconnects. Only explicit user actions may clear playback.
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
	} else if mainReady && !voiceReady {
		reason = "voice_gateway_unready"
	} else if mainReady && !known {
		reason = "voice_membership_unknown"
	}
	if g.unhealthySince.IsZero() {
		g.unhealthySince = now
		b.log.Warn("voice_unhealthy", "guild_id", id, "main_ready", mainReady, "voice_ready", voiceReady, "cache_known", known, "same_channel", sameChannel, "reason", reason)
	}
	// The main gateway reconnects independently. Its cache
	// is not evidence that the Voice websocket or UDP transport failed.
	if terminal || !voiceReady {
		if terminal || now.Sub(g.unhealthySince) >= 5*time.Second {
			if !g.player.State().VoiceSuspended {
				fields := append(g.player.VoiceLifecycleFields(), "channel_id", g.channel, "component", "watchdog", "origin", "checkVoiceHealth", "reason", reason, "previous_state", "active", "next_state", "suspended", "main_ready", mainReady, "voice_ready", voiceReady, "cache_known", known, "same_channel", sameChannel, "duration_ms", now.Sub(g.unhealthySince).Milliseconds())
				b.log.Warn("watchdog_voice_action", fields...)
			}
			g.player.SuspendVoice()
		}
	}
	return true
}
