package discord

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/storage"
)

// CatalogDiscordNotifier uses the same REST adapter as all bot messages. It can
// point to an isolated HTTP server through rest.WithURL in integration tests.
type CatalogDiscordNotifier struct {
	REST   rest.Rest
	UserID snowflake.ID
}

func inaccessibleCatalogChannel(err error) bool {
	var e *rest.Error
	return errors.As(err, &e) && e.Response != nil && (e.Response.StatusCode == http.StatusForbidden || e.Response.StatusCode == http.StatusNotFound)
}
func (n CatalogDiscordNotifier) Eligible(ctx context.Context, c storage.CatalogChannel) (bool, error) {
	guild, err := snowflake.Parse(c.Guild)
	if err != nil {
		return false, err
	}
	channelID, err := snowflake.Parse(c.Channel)
	if err != nil {
		return false, err
	}
	channel, err := n.REST.GetChannel(channelID, rest.WithCtx(ctx))
	if inaccessibleCatalogChannel(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	gc, ok := channel.(d.GuildChannel)
	if !ok || gc.GuildID() != guild {
		return false, nil
	}
	if channel.Type() != d.ChannelTypeGuildText && channel.Type() != d.ChannelTypeGuildNews {
		return false, nil
	}
	member, err := n.REST.GetMember(guild, n.UserID, rest.WithCtx(ctx))
	if inaccessibleCatalogChannel(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if member.CommunicationDisabledUntil != nil && time.Now().Before(*member.CommunicationDisabledUntil) {
		return false, nil
	}
	roles, err := n.REST.GetRoles(guild, rest.WithCtx(ctx))
	if inaccessibleCatalogChannel(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var permissions d.Permissions
	for _, role := range roles {
		if role.ID == guild || slices.Contains(member.RoleIDs, role.ID) {
			permissions |= role.Permissions
		}
	}
	if permissions.Has(d.PermissionAdministrator) {
		return true, nil
	}
	overwrites := gc.PermissionOverwrites()
	if everyone, ok := overwrites.Role(guild); ok {
		permissions = permissions.Remove(everyone.Deny).Add(everyone.Allow)
	}
	var deny, allow d.Permissions
	for _, roleID := range member.RoleIDs {
		if roleID == guild {
			continue
		}
		if role, ok := overwrites.Role(roleID); ok {
			deny |= role.Deny
			allow |= role.Allow
		}
	}
	permissions = permissions.Remove(deny).Add(allow)
	if overwrite, ok := overwrites.Member(n.UserID); ok {
		permissions = permissions.Remove(overwrite.Deny).Add(overwrite.Allow)
	}
	return permissions.Has(d.PermissionViewChannel, d.PermissionSendMessages), nil
}
func (n CatalogDiscordNotifier) Send(ctx context.Context, c storage.CatalogChannel, key, message string) error {
	id, err := snowflake.Parse(c.Channel)
	if err != nil {
		return err
	}
	_, err = n.REST.CreateMessage(id, d.MessageCreate{Content: message, Nonce: key, EnforceNonce: true, AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ctx))
	return err
}
func (b *Bot) watchCatalogAlerts(db *storage.DB) {
	defer b.workers.Done()
	monitor := storage.CatalogAlertMonitor{DB: db, Log: b.log, Notifier: CatalogDiscordNotifier{REST: b.client.Rest, UserID: b.client.ApplicationID}}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case now := <-ticker.C:
			ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
			err := monitor.Poll(ctx, now)
			cancel()
			if err != nil && b.ctx.Err() == nil {
				b.log.Warn("catalog_alert_retry_pending", "error", err)
			}
		}
	}
}
