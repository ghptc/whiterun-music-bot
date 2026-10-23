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
	return c.Conn.Open(ctx, channel, mute, deaf)
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
