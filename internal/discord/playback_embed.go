package discord

import (
	"context"
	"fmt"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func nowContainer(state player.State) d.ContainerComponent {
	if state.Current == nil {
		return musicContainer("Bardão · Now playing", d.TextDisplayComponent{Content: "Nothing playing."})
	}
	detail := "Playing"
	if state.Paused {
		detail = "Paused"
	} else if !state.CurrentStarted {
		detail = ""
	}
	if state.Current.Duration > 0 {
		seconds := int(state.Current.Duration.Seconds())
		duration := fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
		if seconds >= 3600 {
			duration = fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
		}
		if detail != "" {
			detail += " · "
		}
		detail += duration
	}
	body := []d.ContainerSubComponent{songSection(*state.Current, "", detail)}
	entries := queueEntries(state)
	if len(entries) > 0 {
		next := musicLabel(entries[0].Track.Title, 100)
		if next != "" {
			body = append(body, d.TextDisplayComponent{Content: "-# Up next: " + next})
		}
	}
	return musicContainer("Bardão · Now playing", body...)
}
func playbackMessage(event player.PlaybackEvent) d.MessageCreate {
	if event.SkippedReason != "" {
		return d.MessageCreate{Content: playbackText(event), AllowedMentions: &d.AllowedMentions{}}
	}
	state := player.State{Current: &event.Track, CurrentStarted: true}
	if event.Next != nil {
		state.Entries = []player.QueueEntry{{Track: *event.Next}}
	}
	message := d.NewMessageCreateV2(nowContainer(state))
	message.AllowedMentions = &d.AllowedMentions{}
	return message
}
func (b *Bot) replyNow(e *events.ApplicationCommandInteractionCreate, state player.State) {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	if _, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), componentUpdate(nowContainer(state)), rest.WithCtx(ctx)); err != nil {
		b.log.Warn("now_delivery_failed", "error", err)
	}
}
func artworkInvalid(t media.Track) bool {
	return (t.SpotifyArtworkURL != "" && media.SafeArtworkURL(t.SpotifyArtworkURL) == "") || (t.YouTubeThumbnailURL != "" && media.SafeArtworkURL(t.YouTubeThumbnailURL) == "")
}
