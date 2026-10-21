package discord

import (
	"github.com/disgoorg/disgo/gateway"
	"whiterun/internal/player"
)

type Health struct {
	GatewayConnected  bool                     `json:"gateway_connected"`
	WorkersResponsive bool                     `json:"workers_responsive"`
	Players           map[string]player.Health `json:"players"`
}

// Health is bounded even if a guild or its playback worker is deadlocked.
// Gateway outages are degraded service, not grounds for a restart loop.
func (b *Bot) Health() Health {
	h := Health{WorkersResponsive: true, Players: map[string]player.Health{}}
	if b.client.Gateway != nil {
		h.GatewayConnected = b.client.Gateway.Status() == gateway.StatusReady
	}
	if !b.mu.TryLock() {
		h.WorkersResponsive = false
		return h
	}
	defer b.mu.Unlock()
	for id, g := range b.guilds {
		ph := g.player.Health()
		h.Players[id.String()] = ph
		if !ph.Responsive {
			h.WorkersResponsive = false
		}
	}
	return h
}
