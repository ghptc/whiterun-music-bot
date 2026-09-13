package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
)

func commands() []d.ApplicationCommandCreate {
	result := make([]d.ApplicationCommandCreate, 0, 6)
	for _, c := range []struct{ name, description string }{
		{"summon", "Summon the Bard to your voice channel."}, {"play", "Find a song and add it to the queue."},
		{"skip", "Skip the current song."}, {"queue", "Show the current song and queue."},
		{"stop", "Stop playback and clear the queue."}, {"leave", "Dismiss the Bard and clear the queue."},
	} {
		command := d.SlashCommandCreate{Name: c.name, Description: c.description, Contexts: []d.InteractionContextType{d.InteractionContextTypeGuild}}
		if c.name == "play" {
			command.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionString{Name: "query", Description: "Song and artist, requested version, or YouTube video URL", Required: true, MaxLength: new(500)}}
		}
		result = append(result, command)
	}
	return result
}
func (b *Bot) command(e *events.ApplicationCommandInteractionCreate) {
	ack, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	err := e.DeferCreateMessage(true, rest.WithCtx(ack))
	cancel()
	if err != nil {
		b.log.Warn("command acknowledgement failed", "error", err)
		return
	}
	reply := func(content string) {
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		defer cancel()
		_, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), d.MessageUpdate{Content: &content, AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ctx))
		if err != nil {
			b.log.Warn("command response failed", "error", err)
		}
	}
	if e.GuildID() == nil {
		reply("Summon the Bard in a server.")
		return
	}
	data, ok := e.Data.(d.SlashCommandInteractionData)
	if !ok {
		reply("Use one of the Bard's slash commands.")
		return
	}
	id := *e.GuildID()
	g := b.getGuild(id)
	if g == nil {
		reply("The Bard is shutting down.")
		return
	}
	if data.CommandName() == "queue" {
		reply(queueText(g))
		return
	}
	g.mu.Lock()
	state, ok := b.client.Caches.VoiceState(id, e.User().ID)
	if !ok || state.ChannelID == nil {
		g.mu.Unlock()
		reply("Join a voice channel first, traveler.")
		return
	}
	channel := *state.ChannelID
	if g.conn != nil && g.channel != channel {
		g.mu.Unlock()
		reply("Join the Bard's voice channel to command him.")
		return
	}
	name := data.CommandName()
	if name == "summon" || name == "play" {
		if g.conn == nil {
			ctx, cancel := context.WithTimeout(b.ctx, 15*time.Second)
			conn := b.client.VoiceManager.CreateConn(id)
			err := conn.Open(ctx, channel, false, true)
			cancel()
			if err != nil {
				cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
				conn.Close(cleanup)
				c()
				g.mu.Unlock()
				b.log.Warn("voice join failed", "guild_id", id, "error", err)
				reply("The Bard cannot join. Check View Channel, Connect, and Speak permissions.")
				return
			}
			if err := g.player.Attach(transport{conn: conn}); err != nil {
				cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
				conn.Close(cleanup)
				c()
				g.mu.Unlock()
				reply(err.Error())
				return
			}
			g.conn = conn
			g.channel = channel
		}
	}
	switch name {
	case "summon":
		g.mu.Unlock()
		reply("The Bard answers your summons.")
	case "play":
		ticket := g.player.Ticket()
		searchCtx := g.searchCtx
		g.mu.Unlock()
		select {
		case b.searches <- struct{}{}:
		default:
			reply("The Bard is searching for other songs. Try again shortly.")
			return
		}
		track, err := b.resolver.Resolve(searchCtx, data.String("query"))
		<-b.searches
		if err != nil {
			if searchCtx.Err() != nil {
				reply("Playback changed while searching. Ask the Bard again.")
			} else if errors.Is(err, media.ErrNoSong) {
				reply(media.ErrNoSong.Error())
			} else {
				b.log.Warn("song resolution failed", "guild_id", id, "error", err)
				reply("The Bard could not resolve that song. Use a song name or a single YouTube video URL, and try again.")
			}
			return
		}
		g.mu.Lock()
		// The requester may have left while YouTube was resolving.
		now, ok := b.client.Caches.VoiceState(id, e.User().ID)
		if !ok || now.ChannelID == nil || *now.ChannelID != g.channel {
			g.mu.Unlock()
			reply("Join the Bard's voice channel and ask again.")
			return
		}
		err = g.player.Enqueue(track, ticket)
		g.mu.Unlock()
		if err != nil {
			reply(err.Error())
			return
		}
		reply("Queued: " + title(track.Title))
	case "skip":
		skipped := g.player.Skip()
		g.mu.Unlock()
		if skipped {
			reply("On to the next song.")
		} else {
			reply("The Bard is not playing.")
		}
	case "stop":
		g.resetSearches(b.ctx)
		g.player.Stop()
		g.mu.Unlock()
		reply("Playback stopped. The queue is clear.")
	case "leave":
		g.resetSearches(b.ctx)
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		g.player.Leave(ctx)
		cancel()
		g.conn = nil
		g.channel = 0
		g.mu.Unlock()
		reply("The Bard takes his leave.")
	default:
		g.mu.Unlock()
		reply("That command is not in the Bard's repertoire.")
	}
}
func title(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("*", "", "_", "", "`", "", "~", "", "<", "", ">", "").Replace(s)
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:77]) + "..."
	}
	return s
}
func queueText(g *guild) string {
	current, queued := g.player.Snapshot()
	var out strings.Builder
	if current == nil {
		out.WriteString("Now: nothing playing.\n")
	} else {
		fmt.Fprintf(&out, "Now: %s\n", title(current.Title))
	}
	if len(queued) == 0 {
		out.WriteString("The queue is empty.")
	} else {
		for i, t := range queued {
			if i == 10 {
				fmt.Fprintf(&out, "…and %d more.", len(queued)-10)
				break
			}
			fmt.Fprintf(&out, "%d. %s\n", i+1, title(t.Title))
		}
	}
	return out.String()
}
