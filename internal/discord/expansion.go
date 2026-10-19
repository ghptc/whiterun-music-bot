package discord

import (
	"context"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"strings"
	"time"
	"whiterun/internal/media"
)

func expansionCommands() []d.ApplicationCommandCreate {
	result := []d.ApplicationCommandCreate{}
	for _, c := range []struct{ name, description string }{
		{"remove", "Remove an upcoming song."}, {"move", "Move an upcoming song."}, {"shuffle", "Shuffle upcoming songs."},
		{"jump", "Skip directly to an upcoming song."}, {"replay", "Restart the current song."}, {"loop", "Choose song or queue repeat."},
		{"seek", "Request a timestamp in the current song."}, {"autoplay", "Set automatic recommendation preference."},
		{"lyrics", "Check lyrics availability for the current song."}, {"history", "Show recently played songs."}, {"help", "Read the Bard's repertoire."},
	} {
		cmd := d.SlashCommandCreate{Name: c.name, Description: c.description, Contexts: []d.InteractionContextType{d.InteractionContextTypeGuild}}
		position := func(name string) d.ApplicationCommandOption {
			return d.ApplicationCommandOptionInt{Name: name, Description: "Upcoming queue position", Required: true, MinValue: new(1), MaxValue: new(1000)}
		}
		switch c.name {
		case "remove", "jump":
			cmd.Options = []d.ApplicationCommandOption{position("position")}
		case "move":
			cmd.Options = []d.ApplicationCommandOption{position("from"), position("to")}
		case "loop":
			cmd.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionString{Name: "mode", Description: "Repeat mode", Required: true, Choices: []d.ApplicationCommandOptionChoiceString{{Name: "off", Value: "off"}, {Name: "track", Value: "track"}, {Name: "queue", Value: "queue"}}}}
		case "seek":
			cmd.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionString{Name: "position", Description: "MM:SS or HH:MM:SS", Required: true, MaxLength: new(16)}}
		case "autoplay":
			cmd.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionBool{Name: "enabled", Description: "Enable automatic recommendations when available", Required: true}}
		case "history":
			cmd.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionInt{Name: "limit", Description: "Recent songs (default 10)", MinValue: new(1), MaxValue: new(20)}}
		}
		result = append(result, cmd)
	}
	return result
}

// Static content: no player, database, network, or provider access.
func helpEmbed() d.Embed {
	return d.Embed{Title: "The Bard's Repertoire", Color: 0x80745B, Fields: []d.EmbedField{
		{Name: "PLAYBACK", Value: "/play — Request a song or collection.\n/now — Show the current song.\n/pause — Pause the music.\n/resume — Resume the music.\n/previous — Return to the previous song.\n/skip — Skip songs.\n/jump — Skip to a queue position.\n/replay — Restart the current song.\n/loop — Repeat a song or the queue.\n/seek — Check a timestamp; currently unavailable."},
		{Name: "QUEUE", Value: "/queue — View upcoming songs.\n/remove — Remove an upcoming song.\n/move — Reorder an upcoming song.\n/shuffle — Shuffle upcoming songs.\n/clear — Clear upcoming songs."},
		{Name: "EXTRAS", Value: "/autoplay — Save a preference; recommendations unavailable.\n/lyrics — Check lyrics; licensed provider unavailable.\n/history — View recently played songs."},
		{Name: "TAVERN", Value: "/summon — Call the Bard to your channel.\n/stop — Stop music and clear the queue.\n/leave — Dismiss the Bard.\n/help — Read this repertoire."},
	}}
}
func (b *Bot) replyHelp(e *events.ApplicationCommandInteractionCreate) {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	content := ""
	embeds := []d.Embed{helpEmbed()}
	if _, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), d.MessageUpdate{Content: &content, Embeds: &embeds, AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ctx)); err != nil {
		b.log.Warn("help response failed", "error", err)
	}
}

type historyReader interface {
	PlaybackHistory(context.Context, string, int) ([]media.Track, error)
}

func (b *Bot) historyText(id string, limit int) string {
	if limit < 1 || limit > 20 {
		return "History limit must be between 1 and 20."
	}
	store, ok := b.store.(historyReader)
	if !ok {
		return "Playback history is unavailable: SQLite is not configured."
	}
	ctx, cancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer cancel()
	tracks, err := store.PlaybackHistory(ctx, id, limit)
	if err != nil {
		return "Playback history is temporarily unavailable."
	}
	if len(tracks) == 0 {
		return "No songs have played in this tavern yet."
	}
	var text strings.Builder
	for i, t := range tracks {
		fmt.Fprintf(&text, "%d. %s\n", i+1, queueClip(trackTitle(t), 85))
	}
	return text.String()
}
