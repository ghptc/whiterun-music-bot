package discord

import (
	"context"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
)

func (c *voiceConn) Open(ctx context.Context, channel snowflake.ID, mute, deaf bool) error {
	c.joinMu.Lock()
	c.opening, c.joined, c.joinChannel = true, false, channel
	c.joinMu.Unlock()
	defer func() { c.joinMu.Lock(); c.opening = false; c.joinMu.Unlock() }()
	err := c.Conn.Open(ctx, channel, mute, deaf)
	if c.log != nil {
		next, reason := "ready", "open_completed"
		if err != nil {
			next, reason = "unready", "open_failed"
		}
		c.log.Info("voice_lifecycle_state_changed", append(c.voiceFields(), "component", "voice_conn", "origin", "Open", "reason", reason, "previous_state", "opening", "next_state", next)...)
	}
	return err
}

func (c *voiceConn) HandleVoiceStateUpdate(update gateway.EventVoiceStateUpdate) {
	if update.GuildID != c.guildID || update.UserID != c.userID {
		return
	}
	c.joinMu.Lock()
	// A leave echo from the disposed connection can arrive after CreateConn.
	// Before the new join is confirmed, a nil state cannot establish a new
	// session. Ignoring it does not authorize audio: Open still needs the real
	// join, UDP discovery and encryption readiness, or times out.
	staleLeave := c.opening && !c.joined && update.ChannelID == nil
	localClose := c.closing
	if update.ChannelID != nil && *update.ChannelID == c.joinChannel {
		c.joined = true
	}
	c.joinMu.Unlock()
	if staleLeave {
		if c.log != nil {
			c.log.Info("voice_session_state_changed", "state", "awaiting_join", "reason", "pre_join_leave_echo_ignored")
		}
		return
	}
	// After a confirmed join, removal must invalidate the session immediately.
	if update.ChannelID == nil && c.log != nil {
		event, reason, previous := "voice_disconnect_remote", "membership_removed_actor_unknown", "joined"
		if localClose {
			event, reason, previous = "voice_lifecycle_state_changed", "local_close_echo", "closing"
		}
		c.log.Warn(event, append(c.voiceFields(), "component", "voice_conn", "origin", "gateway_voice_state_update", "reason", reason, "previous_state", previous, "next_state", "removed")...)
	}
	if update.ChannelID == nil && c.dave != nil {
		_ = c.dave.Close()
	}
	c.Conn.HandleVoiceStateUpdate(update)
}

// Gateway failure must not eagerly emit a guild leave: its echo can invalidate
// a replacement, and membership is needed to distinguish recovery from a kick.
// The closed gateway is never reusable. Player recovery negotiates a fresh
// session; normal idle/leave cleanup still closes everything when no audio waits.
func (c *voiceConn) gatewayLost() {
	_ = c.Conn.UDP().Close()
	if c.dave != nil {
		_ = c.dave.Close()
	}
	if c.log != nil {
		c.log.Warn("voice_session_state_changed", "state", "transport_invalid", "reason", "gateway_closed", "guild_membership", "preserved_until_recovery_or_leave")
	}
}

// Snapshot callback is copied under the join mutex and invoked without it.
func (c *voiceConn) voiceFields() []any {
	c.joinMu.Lock()
	snapshot, channel := c.lifecycleFields, c.joinChannel
	c.joinMu.Unlock()
	fields := []any{"incident_id", c.correlationID, "guild_id", c.guildID, "channel_id", channel}
	if snapshot != nil {
		fields = append(fields, snapshot()...)
	}
	return fields
}
