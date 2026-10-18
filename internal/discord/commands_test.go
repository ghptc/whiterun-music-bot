package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
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
	messages chan sentMessage
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
	r := &recordingRest{messages: make(chan sentMessage, 256)}
	b := &Bot{ctx: ctx, cancel: cancel, log: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &bot.Client{ApplicationID: 2, Rest: r, Caches: cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates, cache.FlagMembers))}, guilds: make(map[snowflake.ID]*guild)}
	t.Cleanup(func() {
		cancel()
		b.workers.Wait()
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
		if !ok || message.Content != "The Bard searches Skyrim for your song..." || message.Flags != 0 || message.AllowedMentions == nil {
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

type commandSpotify struct{ t *testing.T }

func (s commandSpotify) Collection(_ context.Context, kind, id string) (media.Collection, error) {
	if kind != "playlist" || id != "37i9dQZF1DZ06evO1sJmec" {
		s.t.Fatalf("incorrect Spotify route: %s %s", kind, id)
	}
	return media.Collection{Tracks: []media.Track{{Title: "First", Artist: "Artist"}, {Title: "Missing", Artist: "Artist"}, {Title: "Last", Artist: "Artist"}}}, nil
}
func TestPlayCollectionAndClearCommands(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	g.player.Close(context.Background())
	started := make(chan string, 4)
	release := make(chan struct{}, 4)
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, track media.Track, _ player.Voice) error {
		started <- track.Title
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	c := &dummyConn{}
	g.conn = c
	g.channel = 5
	if err := g.player.Attach(transport{conn: c}); err != nil {
		t.Fatal(err)
	}
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
	b.searches = make(chan struct{}, 4)
	b.resolver = media.CollectionResolver{Spotify: commandSpotify{t: t}, Search: func(_ context.Context, q string) (media.Track, error) {
		if strings.Contains(q, "Missing") {
			return media.Track{}, media.ErrNoSong
		}
		return media.Track{URL: "https://youtube.com/watch?v=abcdefghijk"}, nil
	}}
	e := interaction(t, "play", true)
	raw := `{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"3","username":"tester"},"data":{"id":"4","name":"play","type":1,"options":[{"name":"query","type":3,"value":"https://open.spotify.com/playlist/37i9dQZF1DZ06evO1sJmec?si=1f277a033f9b4f28"}]}}`
	if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
	b.command(e)
	if r.response != "Queued: Artist — First. Resolving the remaining 2 tracks… Use /queue for progress." {
		t.Fatal(r.response)
	}
	select {
	case title := <-started:
		if title != "First" {
			t.Fatal(title)
		}
	case <-time.After(time.Second):
		t.Fatal("did not start")
	}
	waitForCollections(t, g.player)
	b.command(interaction(t, "queue", true))
	if !strings.Contains(r.response, "Artist — First") || !strings.Contains(r.response, "Artist — Last") || !strings.Contains(r.response, "1 tracks remaining") {
		t.Fatal(r.response)
	}
	// A second playlist appends without interrupting the first, preserving duplicates.
	b.command(e)
	waitForCollections(t, g.player)
	if g.player.Length() != 3 {
		t.Fatal(g.player.Length())
	}
	release <- struct{}{}
	select {
	case title := <-started:
		if title != "Last" {
			t.Fatal(title)
		}
	case <-time.After(time.Second):
		t.Fatal("did not advance")
	}
	b.command(interaction(t, "clear", true))
	if r.response != "Cleared 2 tracks from the queue." {
		t.Fatal(r.response)
	}
	current, queued := g.player.Snapshot()
	if current == nil || current.Title != "Last" || len(queued) != 0 {
		t.Fatal("clear changed current")
	}
	b.command(interaction(t, "clear", true))
	if r.response != "The queue is already empty." {
		t.Fatal(r.response)
	}
}

func TestClearRegistered(t *testing.T) {
	for _, command := range commands() {
		if command.CommandName() == "clear" {
			return
		}
	}
	t.Fatal("clear not registered")
}

func TestPauseResumeNowAndLeaveCommands(t *testing.T) {
	b, r := testBot(t)
	g, c := idleGuild(t, b, 1)
	listener(b, 1, 3, false)
	started := make(chan struct{})
	g.player.Close(context.Background())
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, _ player.Voice) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	c = &dummyConn{}
	g.conn = c
	if err := g.player.Attach(transport{conn: c}); err != nil {
		t.Fatal(err)
	}
	track := media.Track{Title: "505", Artist: "Arctic Monkeys", Album: "Favourite Worst Nightmare", RequestedSource: "spotify", Duration: 253 * time.Second}
	if err := g.player.EnqueueMany([]media.Track{track, {Title: "Next"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not playing")
	}
	for _, tt := range []struct{ command, want string }{
		{"pause", "Playback paused."}, {"pause", "already paused"}, {"now", "Paused: Arctic Monkeys — 505"}, {"queue", "Paused: Arctic Monkeys — 505"}, {"resume", "Playback resumed."}, {"resume", "already playing"}, {"pause", "Playback paused."}, {"leave", "takes his leave"},
	} {
		b.command(interaction(t, tt.command, true))
		if !strings.Contains(r.response, tt.want) {
			t.Fatalf("%s: %s", tt.command, r.response)
		}
		if tt.command == "now" && (!strings.Contains(r.response, "Spotify metadata") || !strings.Contains(r.response, "4:13") || strings.Contains(r.response, "Next")) {
			t.Fatal(r.response)
		}
	}
	if !c.closed || g.player.State().Paused || g.player.State().Current != nil || g.player.Length() != 0 {
		t.Fatal("leave retained paused session")
	}
	b.command(interaction(t, "pause", true))
	if !strings.Contains(r.response, "not playing") {
		t.Fatal(r.response)
	}
}
func TestNewCommandsRegisteredAndNowNeedsNoVoice(t *testing.T) {
	names := map[string]bool{}
	for _, c := range commands() {
		names[c.CommandName()] = true
	}
	for _, name := range []string{"pause", "resume", "now", "queue"} {
		if !names[name] {
			t.Fatal(name)
		}
	}
	b, r := testBot(t)
	b.command(interaction(t, "now", true))
	if !strings.Contains(r.response, "nothing playing") {
		t.Fatal(r.response)
	}
}
func TestSpotifyErrorsHideDiagnosticDetails(t *testing.T) {
	for _, known := range []error{media.ErrSpotifyAuth, media.ErrSpotifyAccess, media.ErrSpotifyURL, media.ErrSpotifyMatch} {
		err := fmt.Errorf("%w: status=403 path=private", known)
		if got := spotifyErrorText(err); got != known.Error() || strings.Contains(got, "path=") {
			t.Fatal(got)
		}
	}
}

func waitForCollections(t *testing.T, p *player.GuildPlayer) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for len(p.State().Resolving) != 0 {
		select {
		case <-deadline:
			t.Fatal("collection did not finish")
		case <-time.After(time.Millisecond):
		}
	}
}

type sentMessage struct {
	channel snowflake.ID
	message d.MessageCreate
}

func (r *recordingRest) CreateMessage(channel snowflake.ID, message d.MessageCreate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.messages <- sentMessage{channel, message}
	return &d.Message{}, nil
}
