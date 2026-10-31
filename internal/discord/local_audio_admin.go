package discord

import (
	"context"
	"errors"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type localAudioAdmin struct{ guild, channel, owner snowflake.ID }

// ConfigureLocalValidation enables only the fixed fixture on an operator-approved
// destination. It never joins Voice or starts playback during startup.
func (b *Bot) ConfigureLocalValidation(guild, channel snowflake.ID) error {
	if guild == 0 || channel == 0 {
		return errors.New("local validation requires guild and Voice channel IDs")
	}
	if err := b.ConfigureLocalAudio("/opt/bard-validation"); err != nil {
		return err
	}
	b.localAdmin = localAudioAdmin{guild: guild, channel: channel}
	return nil
}

func (b *Bot) syncLocalAudioAdmin(ctx context.Context) error {
	if b.localAdmin.guild == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	app, err := b.client.Rest.GetCurrentApplication(rest.WithCtx(ctx))
	if err != nil {
		return errors.New("local validation application owner lookup failed")
	}
	owner := snowflake.ID(0)
	if app.Owner != nil {
		owner = app.Owner.ID
	}
	if app.Team != nil {
		owner = app.Team.OwnerID
	}
	if owner == 0 {
		return errors.New("local validation requires a verified application owner")
	}
	channel, err := b.client.Rest.GetChannel(b.localAdmin.channel, rest.WithCtx(ctx))
	if err != nil {
		return errors.New("local validation destination lookup failed")
	}
	audio, ok := channel.(d.GuildAudioChannel)
	if !ok || audio.GuildID() != b.localAdmin.guild {
		return errors.New("local validation destination is not a Voice channel in the approved guild")
	}
	command := d.SlashCommandCreate{Name: "audio-check", Description: "Owner only: play the fixed 30-second validation tone in the approved idle Voice channel.", DefaultMemberPermissions: omit.New(new(d.PermissionAdministrator))}
	created, err := b.client.Rest.CreateGuildCommand(b.client.ApplicationID, b.localAdmin.guild, command, rest.WithCtx(ctx))
	if err != nil {
		return errors.New("local validation command registration failed")
	}
	if created.Name() != "audio-check" {
		return errors.New("local validation command registration mismatch")
	}
	b.localAdmin.owner = owner
	b.log.Info("local_audio_admin_ready", "guild_id", b.localAdmin.guild, "channel_id", b.localAdmin.channel, "owner_id", owner, "automatic_playback", false)
	return nil
}

func (b *Bot) adminAudioCheck(e *events.ApplicationCommandInteractionCreate) string {
	cfg := b.localAdmin
	if cfg.owner == 0 || e.User().ID != cfg.owner {
		return "Only the application owner can run this validation."
	}
	if e.GuildID() == nil || *e.GuildID() != cfg.guild {
		return "Local validation is disabled in this guild."
	}
	state, ok := b.client.Caches.VoiceState(cfg.guild, e.User().ID)
	if !ok || state.ChannelID == nil || *state.ChannelID != cfg.channel {
		return "Join the approved validation Voice channel first."
	}
	g := b.getGuild(cfg.guild)
	if g == nil {
		return "The Bard is shutting down."
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	snapshot := g.player.State()
	if g.conn != nil || snapshot.Current != nil || len(snapshot.Entries) > 0 || len(snapshot.Resolving) > 0 || snapshot.ProviderBlocked || snapshot.Halted || snapshot.Autoplay || (snapshot.Loop != "" && snapshot.Loop != "off") {
		return "Validation refused: Voice or playback state is busy. The queue was preserved."
	}
	// Never detach/move an active session, including a session not locally tracked.
	if vs, ok := b.client.Caches.VoiceState(cfg.guild, b.client.ApplicationID); ok && vs.ChannelID != nil {
		return "Validation refused: the bot already has a Voice session."
	}
	track, err := media.LocalTrack("validation.flac", 30*time.Second)
	if err != nil {
		return "Local validation fixture is unavailable."
	}
	track.Timing = media.NewTiming(b.log.With("interaction_id", e.ID(), "guild_id", cfg.guild, "validation", "manual_owner"))
	ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
	defer cancel()
	conn := b.client.VoiceManager.CreateConn(cfg.guild)
	if err := conn.Open(ctx, cfg.channel, false, true); err != nil {
		conn.Close(player.VoiceCloseContext(ctx, "local_validation_join_failed"))
		return "Validation Voice connection failed; queue preserved."
	}
	if err := g.player.Attach(b.playbackTransport(cfg.guild, g, conn)); err != nil {
		conn.Close(player.VoiceCloseContext(ctx, "local_validation_attach_failed"))
		return "Validation could not attach; queue preserved."
	}
	g.conn = conn
	g.channel = cfg.channel
	g.emptySince = time.Time{}
	if err := g.player.Enqueue(track, g.player.Ticket()); err != nil {
		return "Validation could not enqueue; inspect the administrative logs."
	}
	b.log.Info("local_audio_manual_requested", "guild_id", cfg.guild, "channel_id", cfg.channel, "interaction_id", e.ID(), "owner_id", cfg.owner)
	return "Starting the fixed 30-second synthetic tone. No Spotify or YouTube resolution is used. Confirm audibility and EOF manually."
}
