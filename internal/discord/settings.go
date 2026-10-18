package discord

import (
	"context"
	"strconv"
	"time"
)

type settingsStore interface {
	SaveSettings(context.Context, string, map[string]string) error
	LoadSettings(context.Context, string) (map[string]string, error)
}

func (b *Bot) restoreSettings(id string, g *guild) error {
	store, ok := b.store.(settingsStore)
	if !ok {
		return nil
	}
	settings, err := store.LoadSettings(b.ctx, id)
	if err != nil {
		return err
	}
	if raw := settings["announcement_channel"]; raw != "" {
		channel, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		g.announcementChannel.Store(channel)
	}
	return nil
}
func (b *Bot) saveSettings(id string, g *guild) {
	store, ok := b.store.(settingsStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	if err := store.SaveSettings(ctx, id, map[string]string{"announcement_channel": strconv.FormatUint(g.announcementChannel.Load(), 10)}); err != nil {
		b.log.Error("guild_settings_write_failed", "guild_id", id, "error", err)
	}
}
