package discord

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/player"
)

type recoveringTransport struct {
	transport
	recover func(context.Context) (player.Voice, error)
}

func (v recoveringTransport) Recover(ctx context.Context) (player.Voice, error) {
	return v.recover(ctx)
}

func (b *Bot) playbackTransport(id snowflake.ID, g *guild, conn voice.Conn) player.Voice {
	if c, ok := conn.(*voiceConn); ok {
		c.joinMu.Lock()
		c.lifecycleFields = g.player.VoiceLifecycleFields
		c.joinMu.Unlock()
	}
	brokenUDP := new(atomic.Bool)
	return recoveringTransport{transport: transport{conn: conn, brokenUDP: brokenUDP}, recover: func(ctx context.Context) (player.Voice, error) {
		// Never wait for orchestration while a leave/shutdown is waiting for the
		// scheduler. Cancellation remains observable while acquiring this lock.
		if err := lockVoice(ctx, g); err != nil {
			return nil, err
		}
		if ctx.Err() != nil || b.ctx.Err() != nil || g.channel == 0 {
			g.mu.Unlock()
			return nil, context.Canceled
		}
		if b.client.Gateway.Status() != gateway.StatusReady {
			g.mu.Unlock()
			return nil, errors.New("main gateway unavailable")
		}
		channel := g.channel
		// Never rejoin after removal/move or when the membership cache is unknown.
		state, known := b.client.Caches.VoiceState(id, b.client.ID())
		if !known || state.ChannelID == nil || *state.ChannelID != channel {
			g.mu.Unlock()
			return nil, errors.New("voice membership not authorized; explicit summon required")
		}
		if !brokenUDP.Load() && g.conn != nil && g.conn.Gateway().Status() == voice.StatusReady {
			candidate := g.conn
			g.mu.Unlock()
			if err := confirmVoiceReady(ctx, candidate); err == nil {
				if err := lockVoice(ctx, g); err != nil {
					return nil, err
				}
				state, known := b.client.Caches.VoiceState(id, b.client.ID())
				authorized := g.conn == candidate && g.channel == channel && known && state.ChannelID != nil && *state.ChannelID == channel
				g.mu.Unlock()
				if !authorized {
					return nil, errors.New("voice membership changed during readiness recovery")
				}
				b.log.Info("voice_session_state_changed", "guild_id", id, "state", "preserved", "reason", "local_readiness_confirmed")
				return b.playbackTransport(id, g, candidate), nil
			}
			if err := lockVoice(ctx, g); err != nil {
				return nil, err
			}
			if g.conn != candidate || g.channel != channel {
				g.mu.Unlock()
				return nil, context.Canceled
			}
		}
		if g.conn != nil {
			b.log.Warn("voice_session_state_changed", "guild_id", id, "state", "recreating", "udp_failed", brokenUDP.Load(), "reason", "transport_not_reusable")
			cleanup, cancel := context.WithTimeout(ctx, 3*time.Second)
			g.conn.Close(player.VoiceCloseContext(cleanup, "recovery_transport_not_reusable"))
			cancel()
		}
		// Close removes the old manager entry. CreateConn now constructs a new
		// gateway, UDP transport, and DAVE session instead of resuming invalid MLS.
		candidate := b.client.VoiceManager.CreateConn(id)
		g.conn = candidate
		g.mu.Unlock()
		err := candidate.Open(ctx, channel, false, true)
		if err == nil {
			err = confirmVoiceReady(ctx, candidate)
		}
		if err != nil || ctx.Err() != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			candidate.Close(player.VoiceCloseContext(cleanup, "recovery_candidate_failed"))
			stop()
		}
		if lockErr := lockVoice(ctx, g); lockErr != nil {
			// The canceled candidate was closed above. Never wait behind a
			// leave/shutdown that is waiting for this scheduler to finish.
			return nil, lockErr
		}
		defer g.mu.Unlock()
		if g.conn != candidate || g.channel != channel || ctx.Err() != nil {
			return nil, context.Canceled
		}
		if err != nil {
			return nil, err
		}
		b.log.Info("voice_fresh_session_ready", "guild_id", id, "voice_ready", true, "dave_fresh", true)
		return b.playbackTransport(id, g, candidate), nil
	}}
}

func lockVoice(ctx context.Context, g *guild) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if g.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func confirmVoiceReady(ctx context.Context, conn voice.Conn) error {
	if conn.Gateway().Status() != voice.StatusReady {
		return errors.New("voice gateway not ready")
	}
	if c, ok := conn.(*voiceConn); ok {
		if err := waitVoiceReady(ctx, c.dave, 12*time.Second); err != nil {
			return err
		}
	}
	// Open completed UDP discovery and SessionDescription installed its key.
	// Speaking also verifies the negotiated gateway can accept outbound messages.
	return conn.SetSpeaking(ctx, voice.SpeakingFlagNone)
}
