package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func commands() []d.ApplicationCommandCreate {
	result := make([]d.ApplicationCommandCreate, 0, 10)
	for _, c := range []struct{ name, description string }{
		{"summon", "Summon the Bard to your voice channel."}, {"play", "Find a song and add it to the queue."},
		{"now", "Show the current song."}, {"pause", "Pause the current song."}, {"resume", "Resume the current song."},
		{"previous", "Play the previous song from the beginning."}, {"skip", "Skip the current song."}, {"queue", "Show the current song and queue."},
		{"clear", "Clear upcoming tracks without stopping playback."}, {"stop", "Stop playback and clear the queue."}, {"leave", "Dismiss the Bard and clear the queue."},
	} {
		command := d.SlashCommandCreate{Name: c.name, Description: c.description, Contexts: []d.InteractionContextType{d.InteractionContextTypeGuild}}
		if c.name == "play" {
			command.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionString{Name: "query", Description: "Song and artist, YouTube video/playlist, or Spotify track/playlist/album URL", Required: true, MaxLength: new(500)}, d.ApplicationCommandOptionBool{Name: "force", Description: "Play exactly this YouTube video; requires a video URL"}}
		}
		if c.name == "queue" {
			command.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionInt{Name: "limit", Description: "Maximum upcoming tracks to show (omit for the complete queue)", MinValue: new(1)}}
		}
		if c.name == "skip" {
			command.Options = []d.ApplicationCommandOption{d.ApplicationCommandOptionInt{Name: "count", Description: "Tracks to skip, including the current track (default 1)", MinValue: new(1), MaxValue: new(player.MaxSkipTracks)}}
		}
		result = append(result, command)
	}
	return append(result, expansionCommands()...)
}
func (b *Bot) command(e *events.ApplicationCommandInteractionCreate) {
	started := time.Now()
	data, slash := e.Data.(d.SlashCommandInteractionData)
	if slash {
		b.log.Info("command_received", "name", data.CommandName(), "command_id", data.CommandID(), "guild_id", e.GuildID())
	}
	var timing *media.Timing
	if slash && data.CommandName() == "play" {
		timing = media.NewTiming(b.log.With("interaction_id", e.ID(), "guild_id", e.GuildID()))
		timing.Event("play_request_start", started, "query", data.String("query"))
		defer func() { timing.Event("play_request_total", started) }()
	}
	ack, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	var err error
	if timing != nil {
		err = e.CreateMessage(d.MessageCreate{Content: "The Bard searches Skyrim for your song...", AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ack))
	} else {
		err = e.DeferCreateMessage(!(slash && data.CommandName() == "queue"), rest.WithCtx(ack))
	}
	cancel()
	timing.Event("discord_acknowledged", started, "error", err)
	if err != nil {
		b.log.Warn("command acknowledgement failed", "interaction_id", e.ID(), "guild_id", e.GuildID(), "error", err)
		return
	}
	reply := func(content string) {
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		defer cancel()
		replyStart := time.Now()
		_, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), d.MessageUpdate{Content: &content, AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(ctx))
		timing.Event("discord_response_complete", replyStart, "error", err)
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
	if data.CommandName() == "help" {
		b.replyHelp(e)
		return
	}
	id := *e.GuildID()
	if data.CommandName() == "history" {
		limit := 10
		if value, present := data.OptInt("limit"); present {
			limit = value
		}
		reply(b.historyText(id.String(), limit))
		return
	}
	g := b.getGuild(id)
	if g == nil {
		reply("The Bard is shutting down.")
		return
	}
	if data.CommandName() == "lyrics" {
		if g.player.State().Current == nil {
			reply("The Bard is not playing.")
		} else {
			reply("Lyrics are unavailable: no licensed lyrics provider is configured.")
		}
		return
	}
	if data.CommandName() == "now" {
		b.replyNow(e, g.player.State())
		return
	}
	if data.CommandName() == "queue" {
		var limit *int
		if value, present := data.OptInt("limit"); present {
			limit = &value
		}
		b.replyQueue(e, g.player.State(), limit)
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
			joinStart := time.Now()
			conn := b.client.VoiceManager.CreateConn(id)
			err := conn.Open(ctx, channel, false, true)
			timing.Event("voice_join_complete", joinStart, "error", err)
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
			g.emptySince = time.Time{}
		}
	}
	switch name {
	case "remove", "move", "shuffle", "jump", "replay", "loop", "seek", "autoplay":
		var err error
		message := "Queue updated."
		switch name {
		case "remove":
			err = g.player.Remove(data.Int("position"))
		case "move":
			err = g.player.Move(data.Int("from"), data.Int("to"))
		case "shuffle":
			err = g.player.Shuffle()
		case "jump":
			err = g.player.Jump(data.Int("position"))
			message = "Jump requested."
		case "replay":
			err = g.player.Replay()
			message = "Restart requested."
		case "loop":
			err = g.player.SetLoop(data.String("mode"))
			message = "Loop mode: " + data.String("mode") + "."
		case "seek":
			err = g.player.Seek(data.String("position"))
		case "autoplay":
			enabled := data.Bool("enabled")
			g.player.SetAutoplay(enabled)
			message = "Autoplay disabled."
			if enabled {
				message = "Autoplay preference saved. Recommendations are unavailable: no supported recommendation provider is configured."
			}
		}
		g.mu.Unlock()
		if err != nil {
			reply(err.Error())
		} else {
			reply(message)
		}
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
		releaseSearch := sync.OnceFunc(func() { <-b.searches })
		defer releaseSearch()
		resolveCtx := media.WithTiming(searchCtx, timing)
		result, progressive, err := b.resolver.PreparePlay(resolveCtx, data.String("query"), data.Bool("force"))
		if err != nil {
			b.log.Warn("media resolution failed", "guild_id", id, "stage", resolutionStage(err), "error", err)
			if searchCtx.Err() != nil {
				reply("Playback changed while searching. Ask the Bard again.")
			} else if message := spotifyErrorText(err); message != "" {
				reply(message)
			} else if errors.Is(err, media.ErrForceURL) || errors.Is(err, media.ErrNoTracks) || errors.Is(err, media.ErrCollectionTooLarge) {
				reply(err.Error())
			} else if errors.Is(err, media.ErrNoSong) {
				reply(media.ErrNoSong.Error())
			} else {
				reply("Could not resolve this song or collection. Check the link and try again.")
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
		for i := range result.Tracks {
			result.Tracks[i].RequestedBy = e.User().ID.String()
		}
		queueStart := time.Now()
		var job *player.CollectionJob
		g.announcementMu.Lock()
		previousChannel := g.announcementChannel.Load()
		if channel := e.Channel(); channel.MessageChannel != nil {
			g.announcementChannel.Store(uint64(channel.ID()))
		}
		if progressive {
			job, err = g.player.BeginCollection(resolveCtx, result, b.resolver.MatchSpotify, ticket)
		} else {
			err = g.player.EnqueueMany(result.Tracks, ticket)
		}
		if err != nil {
			g.announcementChannel.Store(previousChannel)
		}
		g.announcementMu.Unlock()
		if err == nil {
			b.saveSettings(id.String(), g)
		}
		timing.Event("queue_inserted", queueStart, "error", err, "pending", progressive)
		g.mu.Unlock()
		var first player.CollectionFirst
		if err == nil && progressive {
			first = <-job.First
			err = first.Err
		}
		releaseSearch()
		if err != nil {
			reply(err.Error())
			return
		}
		if progressive {
			message := "Queued: " + trackTitle(first.Track)
			if remaining := first.Total - first.Position; remaining > 0 {
				message += fmt.Sprintf(". Resolving the remaining %d tracks… Use /queue for progress.", remaining)
			}
			if first.Skipped > 0 {
				message += fmt.Sprintf(" %d earlier positions were skipped.", first.Skipped)
			}
			reply(message)
		} else if result.IsCollection {
			message := fmt.Sprintf("Added %d tracks to the queue.", len(result.Tracks))
			if result.Skipped > 0 {
				message += fmt.Sprintf(" %d unavailable tracks were skipped.", result.Skipped)
			}
			reply(message)
		} else {
			reply(queuedTrackText(result.Tracks[0]))
		}
	case "pause", "resume":
		paused := name == "pause"
		providerBlocked := g.player.State().ProviderBlocked
		changed, err := g.player.SetPaused(paused)
		g.mu.Unlock()
		if err != nil {
			reply(err.Error())
		} else if paused && changed {
			reply("Playback paused.")
		} else if paused {
			reply("Playback is already paused.")
		} else if changed {
			if providerBlocked {
				reply("YouTube recovery requested. Retrying the same track.")
			} else {
				reply("Playback resumed.")
			}
		} else {
			reply("Playback is already playing.")
		}
	case "previous":
		err := g.player.Previous()
		g.mu.Unlock()
		if err != nil {
			reply(err.Error())
		} else {
			reply("Previous song queued from the beginning.")
		}
	case "skip":
		count := 1
		if value, present := data.OptInt("count"); present {
			count = value
		}
		result, err := g.player.SkipN(count)
		g.mu.Unlock()
		if err != nil {
			reply(err.Error())
		} else {
			reply(skipText(result))
		}
	case "clear":
		g.resetSearches(b.ctx)
		count := g.player.Clear()
		g.mu.Unlock()
		if count == 0 {
			reply("The queue is already empty.")
		} else {
			reply(fmt.Sprintf("Cleared %d tracks from the queue.", count))
		}
	case "stop":
		g.resetSearches(b.ctx)
		g.player.Stop()
		g.mu.Unlock()
		reply("Playback stopped. The queue is clear.")
	case "leave":
		ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
		g.leave(ctx, b.ctx)
		cancel()
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

func skipText(result player.SkipResult) string {
	if result.Skipped == 0 {
		return "The Bard is not playing. The queue is empty."
	}
	word := "tracks"
	if result.Skipped == 1 {
		word = "track"
	}
	if result.Next == nil {
		return fmt.Sprintf("Skipped %d %s. Queue is now empty.", result.Skipped, word)
	}
	label := trackTitle(*result.Next)
	if result.NextPending {
		label += " [resolving]"
	}
	return fmt.Sprintf("Skipped %d %s. Next: %s. %d tracks remaining.", result.Skipped, word, label, result.Remaining)
}

func trackTitle(t media.Track) string {
	if t.Artist != "" {
		return title(t.Artist + " — " + t.Title)
	}
	return title(t.Title)
}

func spotifyErrorText(err error) string {
	for _, known := range []error{media.ErrSpotifyIncomplete, media.ErrSpotifyPageParse, media.ErrSpotifyPageFetch, media.ErrSpotifyURL, media.ErrSpotifyConfig, media.ErrSpotifyAuth, media.ErrSpotifyAccess, media.ErrSpotifyMatch} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return ""
}
func resolutionStage(err error) string {
	switch {
	case errors.Is(err, media.ErrSpotifyURL):
		return "spotify_url"
	case errors.Is(err, media.ErrSpotifyConfig), errors.Is(err, media.ErrSpotifyAuth):
		return "spotify_authorization"
	case errors.Is(err, media.ErrSpotifyIncomplete):
		return "spotify_public_incomplete"
	case errors.Is(err, media.ErrSpotifyPageParse):
		return "spotify_public_parse"
	case errors.Is(err, media.ErrSpotifyPageFetch):
		return "spotify_public_fetch"
	case errors.Is(err, media.ErrSpotifyAccess):
		return "spotify_access"
	case errors.Is(err, media.ErrSpotifyMatch), errors.Is(err, media.ErrNoSong):
		return "track_match"
	default:
		return "metadata_resolution"
	}
}

func queuedTrackText(track media.Track) string {
	if track.ForcedYouTube {
		return "Queued: " + title(track.Title) + " — Direct YouTube"
	}
	return "Queued: " + trackTitle(track)
}
