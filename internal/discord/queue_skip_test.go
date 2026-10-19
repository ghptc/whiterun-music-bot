package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func intInteraction(t *testing.T, command, option string, value int) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	e := interaction(t, command, true)
	raw := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"3","username":"tester"},"data":{"id":"4","name":%q,"type":1,"options":[{"name":%q,"type":4,"value":%d}]}}`, command, option, value)
	if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestQueueAndSkipOptionRegistration(t *testing.T) {
	found := 0
	for _, raw := range commands() {
		c := raw.(d.SlashCommandCreate)
		if c.Name != "skip" && c.Name != "queue" {
			continue
		}
		found++
		if len(c.Options) != 1 {
			t.Fatal(c)
		}
		opt, ok := c.Options[0].(d.ApplicationCommandOptionInt)
		if !ok || opt.Required || opt.MinValue == nil || *opt.MinValue != 1 {
			t.Fatal(opt)
		}
		if c.Name == "skip" && (opt.Name != "count" || opt.MaxValue == nil || *opt.MaxValue != 1001) {
			t.Fatal(opt)
		}
		if c.Name == "queue" && (opt.Name != "limit" || opt.MaxValue != nil) {
			t.Fatal(opt)
		}
	}
	if found != 2 {
		t.Fatal(found)
	}
}
func TestQueueAndBulkSkipCommands(t *testing.T) {
	for _, count := range []int{0, 1, 2, 50, 123, 1001, -1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			b, r := testBot(t)
			g := b.getGuild(1)
			g.player.Close(context.Background())
			started := make(chan string, 124)
			g.player = player.New(b.ctx, b.log, func(ctx context.Context, track media.Track, _ player.Voice) error {
				started <- track.Title
				<-ctx.Done()
				return ctx.Err()
			})
			conn := &dummyConn{}
			g.conn = conn
			g.channel = 5
			if err := g.player.Attach(transport{conn: conn}); err != nil {
				t.Fatal(err)
			}
			b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
			tracks := make([]media.Track, 123)
			for i := range tracks {
				tracks[i] = media.Track{Title: fmt.Sprintf("Song %03d", i+1)}
			}
			if err := g.player.EnqueueMany(tracks, g.player.Ticket()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("not playing")
			}
			b.command(interaction(t, "queue", true))
			if !strings.Contains(r.queueText(), "121. Song 122") || !strings.Contains(r.queueText(), "122. Song 123") || !strings.Contains(r.response, "122 songs up next") {
				t.Fatal(r.response)
			}
			if count == 0 {
				b.command(interaction(t, "skip", true))
			} else {
				b.command(intInteraction(t, "skip", "count", count))
			}
			if count < 0 {
				if !strings.Contains(r.response, "between 1 and") {
					t.Fatal(r.response)
				}
				return
			}
			actual := min(123, max(1, count))
			if !strings.Contains(r.response, fmt.Sprintf("Skipped %d track", actual)) {
				t.Fatal(r.response)
			}
			if actual == 123 {
				if !strings.Contains(r.response, "Queue is now empty") {
					t.Fatal(r.response)
				}
			} else if !strings.Contains(r.response, fmt.Sprintf("Next: Song %03d", actual+1)) {
				t.Fatal(r.response)
			}
		})
	}
}
func TestSkipResponseDoesNotClaimPlaybackStarted(t *testing.T) {
	text := skipText(player.SkipResult{Skipped: 50, Remaining: 73, Next: &media.Track{Title: "51"}})
	if strings.Contains(text, "Now playing") || !strings.Contains(text, "Next: 51") {
		t.Fatal(text)
	}
	if text = skipText(player.SkipResult{}); !strings.Contains(text, "not playing") {
		t.Fatal(text)
	}
}

func TestPublicQueueDuringProgressiveLoading(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	g.player.Close(context.Background())
	started := make(chan struct{})
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, _ player.Voice) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err := g.player.Attach(transport{conn: &dummyConn{}}); err != nil {
		t.Fatal(err)
	}
	tracks := make([]media.Track, 123)
	for i := range tracks {
		tracks[i] = media.Track{Title: fmt.Sprintf("Song %03d", i+1)}
	}
	job, err := g.player.BeginCollection(b.ctx, media.Collection{Tracks: tracks}, func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if first {
			track.URL = "https://youtube.com/watch?v=abcdefghijk"
			return track, nil
		}
		<-ctx.Done()
		return media.Track{}, ctx.Err()
	}, g.player.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first track blocked")
	}
	for _, limit := range []int{0, 1, 10, 500} {
		r.followups = nil
		e := interaction(t, "queue", true)
		if limit > 0 {
			e = intInteraction(t, "queue", "limit", limit)
		}
		acknowledged := false
		e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
			message, ok := data.(d.MessageCreate)
			if typ != d.InteractionResponseTypeDeferredCreateMessage || (data != nil && (!ok || message.Flags&d.MessageFlagEphemeral != 0)) {
				t.Fatal("queue is not public")
			}
			acknowledged = true
			return nil
		}
		b.command(e)
		if !acknowledged || !strings.Contains(r.response, "songs up next") {
			t.Fatal(r.response)
		}
		text := r.queueText()
		end := 122
		if limit > 0 {
			end = min(end, limit)
		}
		for i := 1; i <= 122; i++ {
			if strings.Contains(text, fmt.Sprintf("**%d. Song %03d**", i, i+1)) != (i <= end) {
				t.Fatalf("incorrect pending position %d", i)
			}
		}
	}

	g.player.Clear()
	select {
	case <-job.Done:
	case <-time.After(time.Second):
		t.Fatal("clear leaked workers")
	}
}

func TestOtherCommandResponsesRemainEphemeral(t *testing.T) {
	b, _ := testBot(t)
	for _, name := range []string{"now", "skip", "clear", "stop", "leave", "pause", "resume", "summon"} {
		e := interaction(t, name, true)
		e.Respond = func(_ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
			message, ok := data.(d.MessageCreate)
			if !ok || message.Flags&d.MessageFlagEphemeral == 0 {
				t.Fatalf("%s became public: %#v", name, data)
			}
			return nil
		}
		b.command(e)
	}
}
