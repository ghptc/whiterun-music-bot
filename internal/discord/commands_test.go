package discord

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf16"
	"whiterun/internal/media"
	"whiterun/internal/player"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

type recordingRest struct {
	rest.Rest
	response string
}

func (r *recordingRest) UpdateInteractionResponse(_ snowflake.ID, _ string, m d.MessageUpdate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.response = *m.Content
	return &d.Message{}, nil
}

type dummyConn struct {
	voice.Conn
	closed bool
}

func (c *dummyConn) Close(context.Context) { c.closed = true }
func interaction(t *testing.T, name string, guild bool) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	scope := `"guild_id":"1",`
	if !guild {
		scope = ""
	}
	data := `{"id":"10","application_id":"2","type":2,"token":"test",` + scope + `"user":{"id":"3","username":"tester"},"data":{"id":"4","name":"` + name + `","type":1}}`
	var i d.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(data), &i); err != nil {
		t.Fatal(err)
	}
	return &events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: i, Respond: func(typ d.InteractionResponseType, _ d.InteractionResponseData, _ ...rest.RequestOpt) error {
		if typ != d.InteractionResponseTypeDeferredCreateMessage {
			t.Errorf("not deferred: %v", typ)
		}
		return nil
	}}
}
func testBot(t *testing.T) (*Bot, *recordingRest) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &recordingRest{}
	b := &Bot{ctx: ctx, cancel: cancel, log: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &bot.Client{ApplicationID: 2, Rest: r, Caches: cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates))}, guilds: make(map[snowflake.ID]*guild)}
	t.Cleanup(func() {
		cancel()
		for _, g := range b.guilds {
			g.player.Close(context.Background())
		}
	})
	return b, r
}
func TestCommandsRequireGuildAndVoice(t *testing.T) {
	b, r := testBot(t)
	b.command(interaction(t, "summon", false))
	if !strings.Contains(r.response, "server") {
		t.Fatal(r.response)
	}
	b.command(interaction(t, "summon", true))
	if !strings.Contains(r.response, "Join a voice channel") {
		t.Fatal(r.response)
	}
	b.command(interaction(t, "queue", true))
	if !strings.Contains(r.response, "queue is empty") {
		t.Fatal(r.response)
	}
}
func TestControlsAndSearchCancellation(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	c := &dummyConn{}
	g.conn = c
	g.channel = 5
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(6))})
	b.command(interaction(t, "stop", true))
	if !strings.Contains(r.response, "Join the Bard's voice channel") {
		t.Fatal(r.response)
	}
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
	b.command(interaction(t, "skip", true))
	if !strings.Contains(r.response, "not playing") {
		t.Fatal(r.response)
	}
	old := g.searchCtx
	b.command(interaction(t, "stop", true))
	if old.Err() == nil || g.searchCtx.Err() != nil {
		t.Fatal("stop did not replace and cancel search context")
	}
	if !strings.Contains(r.response, "queue is clear") {
		t.Fatal(r.response)
	}
	// Attach an idle transport so leave exercises real player detachment.
	if err := g.player.Attach(transport{conn: c}); err != nil {
		t.Fatal(err)
	}
	old = g.searchCtx
	b.command(interaction(t, "leave", true))
	if !c.closed || g.conn != nil || old.Err() == nil {
		t.Fatal("leave retained session")
	}
}
func TestQueueResponseStaysWithinDiscordLimit(t *testing.T) {
	b, _ := testBot(t)
	g := b.getGuild(1)
	g.player.Close(context.Background())
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, _ player.Voice) error { <-ctx.Done(); return ctx.Err() })
	if err := g.player.Attach(transport{conn: &dummyConn{}}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("🎵", 200) + " @everyone"
	for range 50 {
		if err := g.player.Enqueue(media.Track{Title: long}, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(utf16.Encode([]rune(queueText(g)))); n > 2000 {
		t.Fatalf("response has %d UTF-16 units", n)
	}
	if !strings.Contains(queueText(g), "more") {
		t.Fatal("missing overflow count")
	}
}

func TestPlayAcknowledgesWithContentBeforeGuildWork(t *testing.T) {
	b, r := testBot(t)
	e := interaction(t, "play", true)
	acknowledged := false
	e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
		if typ != d.InteractionResponseTypeCreateMessage {
			t.Fatalf("response type %v", typ)
		}
		message, ok := data.(d.MessageCreate)
		if !ok || message.Content != "The Bard searches Skyrim for your song..." || message.Flags != d.MessageFlagEphemeral || message.AllowedMentions == nil {
			t.Fatalf("acknowledgement: %#v", data)
		}
		if len(b.guilds) != 0 || r.response != "" {
			t.Fatal("guild work happened before acknowledgement")
		}
		acknowledged = true
		return nil
	}
	b.command(e)
	if !acknowledged || !strings.Contains(r.response, "Join a voice channel") {
		t.Fatalf("ack=%v response=%s", acknowledged, r.response)
	}
}
