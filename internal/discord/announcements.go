package discord

import (
	"context"
	"fmt"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/player"
)

type announcement struct {
	channel snowflake.ID
	epoch   uint64
	event   player.PlaybackEvent
}

// One bounded worker per guild isolates REST latency from audio. The latest
// accepted /play chooses the channel for future starts. No interaction retained.
func (b *Bot) setupAnnouncements(id snowflake.ID, g *guild) {
	g.announcements = make(chan announcement, 32)
	b.bindAnnouncements(g)
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		for {
			select {
			case <-b.ctx.Done():
				return
			case item := <-g.announcements:
				if item.epoch != g.announcementEpoch.Load() {
					continue
				}
				ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
				_, err := b.client.Rest.CreateMessage(item.channel, d.MessageCreate{Content: playbackText(item.event), AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ctx))
				cancel()
				if err != nil {
					b.log.Warn("playback_announcement_failed", "guild_id", id, "channel_id", item.channel, "sequence", item.event.Sequence, "error", err)
				}
			}
		}
	}()
}
func playbackText(event player.PlaybackEvent) string {
	track := event.Track
	if event.SkippedReason != "" {
		label := "Unavailable"
		if event.FailureStage == "playing" {
			label = "Playback interrupted"
		} else if event.FailureStage == "starting" {
			label = "Playback failed to start"
		}
		return fmt.Sprintf("%s: %s • position %d/%d • Spotify ID: %s\n%s", label, trackTitle(track), track.Position, track.Total, track.SpotifyID, event.SkippedReason)
	}
	icon, source := "▶", "YouTube"
	if track.Live {
		icon, source = "🔴", "YouTube Live"
	} else if track.RequestedSource == "spotify" {
		source = "Spotify metadata • YouTube audio"
	}
	if track.Total > 1 {
		source += fmt.Sprintf(" • %d/%d", track.Position, track.Total)
	}
	if track.SpotifyID != "" {
		source += " • Spotify ID: " + track.SpotifyID
	}
	next := "—"
	if event.NextPending {
		next = "resolving…"
	} else if event.Next != nil {
		next = trackTitle(*event.Next)
	}
	if track.RequestedBy != "" {
		source += " • Requested by <@" + track.RequestedBy + ">"
	}
	return fmt.Sprintf("%s Now Playing\n%s\n%s\nNext: %s", icon, trackTitle(track), source, next)
}

func (b *Bot) bindAnnouncements(g *guild) {
	send := func(event player.PlaybackEvent) {
		g.announcementMu.Lock()
		item := announcement{snowflake.ID(g.announcementChannel.Load()), g.announcementEpoch.Load(), event}
		g.announcementMu.Unlock()
		if item.channel == 0 {
			return
		}
		select {
		case g.announcements <- item:
		default:
			b.log.Warn("playback_announcement_dropped", "sequence", event.Sequence, "reason", "announcement_queue_full")
		}
	}
	g.player.SetOnStart(send)
	g.player.SetOnSkip(send)
}
