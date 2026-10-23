package discord

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type announcement struct {
	channel snowflake.ID
	epoch   uint64
	event   player.PlaybackEvent
	queued  time.Time
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
				if item.event.SkippedReason == "" && !g.player.IsCurrentPlayback(item.event.Sequence) {
					continue
				}
				frameAt := item.event.StartedAt
				if frameAt.IsZero() {
					frameAt = item.queued
				}
				ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
				_, err := b.client.Rest.CreateMessage(item.channel, playbackMessage(item.event), rest.WithCtx(ctx))
				cancel()
				b.log.Info("playback_announcement_delivery", "guild_id", id, "sequence", item.event.Sequence, "first_frame_to_announcement_ms", time.Since(frameAt).Milliseconds(), "artwork_present", media.ArtworkURL(item.event.Track) != "", "spotify_id", item.event.Track.SpotifyID, "video_id", item.event.Track.VideoID, "position", item.event.Track.Position, "failure_stage", item.event.FailureStage, "reason", item.event.SkippedReason, "artwork_metadata_invalid", artworkInvalid(item.event.Track), "success", err == nil)
				if err != nil {
					b.log.Warn("playback_announcement_failed", "guild_id", id, "channel_id", item.channel, "sequence", item.event.Sequence, "error", err)
				}
			}
		}
	}()
}
func playbackText(event player.PlaybackEvent) string {
	if event.FailureStage == "voice" {
		return event.SkippedReason
	}
	label := "Now playing"
	if event.SkippedReason != "" {
		label = "Unavailable"
		if event.FailureStage == "playing" {
			label = "Playback interrupted"
		} else if event.FailureStage == "starting" {
			label = "Playback failed to start"
		}
	}
	return fmt.Sprintf("%s: %s", label, trackTitle(event.Track))
}

func (b *Bot) bindAnnouncements(g *guild) {
	send := func(event player.PlaybackEvent) {
		// Same logical occurrence already has a Now Playing card. Resume is
		// recorded in correlated logs without sending a duplicate card.
		if event.Resumed {
			return
		}
		g.announcementMu.Lock()
		item := announcement{snowflake.ID(g.announcementChannel.Load()), g.announcementEpoch.Load(), event, time.Now()}
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
