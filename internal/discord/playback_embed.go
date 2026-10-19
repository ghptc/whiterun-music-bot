package discord

import (
	"fmt"
	"strings"

	d "github.com/disgoorg/disgo/discord"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func playbackMessage(event player.PlaybackEvent) d.MessageCreate {
	m := d.MessageCreate{AllowedMentions: &d.AllowedMentions{}}
	if event.SkippedReason != "" {
		m.Content = playbackText(event)
		return m
	}
	t := event.Track
	source := "YouTube"
	if t.Live {
		source = "YouTube Live"
	}
	if t.RequestedSource == "spotify" {
		source = "Spotify metadata • YouTube audio"
	}
	if t.Total > 0 {
		source += fmt.Sprintf(" • %d/%d", t.Position, t.Total)
	}
	next := "—"
	if event.NextPending {
		next = "resolving…"
	} else if event.Next != nil {
		next = trackTitle(*event.Next)
	}
	description := queueClip(t.Artist, 256)
	if description == "" {
		description = queueClip(t.Channel, 256)
	}
	description += "\n" + source
	if t.RequestedBy != "" {
		description += "\nRequested by: " + queueClip(t.RequestedBy, 80)
	}
	description += "\nNext: " + queueClip(next, 400)
	title := "▶ Now Playing · " + t.Title
	if t.Live {
		title = "🔴 Now Playing · " + t.Title
	}
	embed := d.Embed{Title: queueClip(title, 256), Description: strings.TrimSpace(description), Color: 0xd6ad60}
	if art := media.ArtworkURL(t); art != "" {
		embed.Thumbnail = &d.EmbedResource{URL: art}
	}
	m.Embeds = []d.Embed{embed}
	return m
}

func artworkInvalid(t media.Track) bool {
	return (t.SpotifyArtworkURL != "" && media.SafeArtworkURL(t.SpotifyArtworkURL) == "") || (t.YouTubeThumbnailURL != "" && media.SafeArtworkURL(t.YouTubeThumbnailURL) == "")
}
