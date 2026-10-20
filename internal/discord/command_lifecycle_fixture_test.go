package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

// Exercise the real Discord transport adapter without a socket or encryption.
// UDP submission here is a controlled boundary, never an audibility claim.
type lifecycleUDP struct {
	voice.UDPConn
	frames atomic.Int64
}

func (*lifecycleUDP) SetWriteDeadline(time.Time) error { return nil }
func (u *lifecycleUDP) Write(p []byte) (int, error)    { u.frames.Add(1); return len(p), nil }

type lifecycleConn struct {
	voice.Conn
	udp    lifecycleUDP
	closed atomic.Bool
}

func (c *lifecycleConn) Open(context.Context, snowflake.ID, bool, bool) error {
	c.closed.Store(false)
	return nil
}
func (c *lifecycleConn) Close(context.Context)                                { c.closed.Store(true) }
func (c *lifecycleConn) UDP() voice.UDPConn                                   { return &c.udp }
func (*lifecycleConn) SetSpeaking(context.Context, voice.SpeakingFlags) error { return nil }

type lifecycleManager struct{ voice.Manager }

func (lifecycleManager) CreateConn(snowflake.ID) voice.Conn { return &lifecycleConn{} }

type lifecycleStore struct {
	mu     sync.Mutex
	states map[string]player.DurableState
	played map[string][]media.Track
}

func (s *lifecycleStore) SaveGuild(_ context.Context, id string, state player.DurableState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(state)
	var clone player.DurableState
	_ = json.Unmarshal(raw, &clone)
	s.states[id] = clone
	return nil
}
func (s *lifecycleStore) LoadGuild(_ context.Context, id string) (player.DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.states[id])
	var state player.DurableState
	_ = json.Unmarshal(raw, &state)
	return state, nil
}
func (s *lifecycleStore) RecordPlayback(_ context.Context, id string, tr media.Track) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.played[id] = append(s.played[id], tr)
	return nil
}
func (s *lifecycleStore) PlaybackHistory(_ context.Context, id string, limit int) ([]media.Track, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []media.Track{}
	for i := len(s.played[id]) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.played[id][i])
	}
	return out, nil
}

type lifecycleSpotify struct{}

func (lifecycleSpotify) Collection(_ context.Context, _, id string) (media.Collection, error) {
	return media.Collection{Tracks: []media.Track{{Title: "request", SpotifyID: id, RequestedSource: "spotify", Position: 1, Total: 1, Duration: time.Minute, SpotifyArtworkURL: "https://i.scdn.co/image/fixture"}}}, nil
}

type lifecycleFixture struct {
	t       *testing.T
	b       *Bot
	r       *recordingRest
	g       *guild
	finish  chan error
	store   player.Store
	model   lifecycleOracle
	serial  int
	cleaned atomic.Int64
	active  atomic.Int64
	overlap atomic.Bool
}

func newLifecycleFixture(t *testing.T, phase string, store player.Store) *lifecycleFixture {
	return newLifecycleFixtureLog(t, phase, store, nil)
}
func newLifecycleFixtureLog(t *testing.T, phase string, store player.Store, log *slog.Logger) *lifecycleFixture {
	t.Helper()
	if store == nil {
		store = &lifecycleStore{states: map[string]player.DurableState{}, played: map[string][]media.Track{}}
	}
	b, r := testBot(t)
	if log != nil {
		b.log = log
	}
	b.store = store
	b.client.VoiceManager = lifecycleManager{}
	b.searches = make(chan struct{}, 4)
	b.resolver = media.CollectionResolver{Spotify: lifecycleSpotify{}, Search: func(context.Context, string) (media.Track, error) {
		return media.Track{URL: "https://www.youtube.com/watch?v=abcdefghijk", VideoID: "abcdefghijk"}, nil
	}}
	g := b.getGuild(1)
	if g == nil {
		t.Fatal("guild initialization failed")
	}
	g.player.Close(context.Background())
	f := &lifecycleFixture{t: t, b: b, r: r, g: g, finish: make(chan error), store: store, model: lifecycleOracle{attached: true, generation: 1}}
	g.player = player.New(b.ctx, b.log.With("guild_id", "1"), f.play)
	if err := g.player.ConfigureStore(store, "1"); err != nil {
		t.Fatal(err)
	}
	conn := &lifecycleConn{}
	if err := g.player.Attach(transport{conn: conn}); err != nil {
		t.Fatal(err)
	}
	g.conn = conn
	g.channel = 5
	b.bindAnnouncements(g)
	listener(b, 1, 3, false)
	listener(b, 1, 4, false)
	if phase != "idle" {
		tracks := []media.Track{}
		for i, name := range []string{"a", "b", "c", "d"} {
			tracks = append(tracks, media.Track{Title: name, SpotifyID: "id-" + name, Position: i + 1, Total: 4, Duration: time.Minute, SpotifyArtworkURL: "https://i.scdn.co/image/" + name})
		}
		if err := g.player.EnqueueMany(tracks, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
		f.model.queue = []string{"a", "b", "c", "d"}
		f.model.settle()
	}
	synctest.Wait()
	if phase == "paused" {
		_, _ = g.player.SetPaused(true)
		f.model.paused = true
	}
	f.check("initial")
	return f
}
func lifecycleOptions(name string) []map[string]any {
	arg := func(n string, typ int, v any) map[string]any {
		return map[string]any{"name": n, "type": typ, "value": v}
	}
	switch name {
	case "play":
		return []map[string]any{arg("query", 3, "https://open.spotify.com/track/0123456789012345678901")}
	case "remove", "jump":
		return []map[string]any{arg("position", 4, 2)}
	case "move":
		return []map[string]any{arg("from", 4, 2), arg("to", 4, 1)}
	case "loop":
		return []map[string]any{arg("mode", 3, "queue")}
	case "seek":
		return []map[string]any{arg("position", 3, "0:15")}
	case "autoplay":
		return []map[string]any{arg("enabled", 5, true)}
	default:
		return nil
	}
}
func (f *lifecycleFixture) interaction(name string) *events.ApplicationCommandInteractionCreate {
	return f.interactionFor(name, 1, 3)
}
func (f *lifecycleFixture) interactionFor(name string, guild, user snowflake.ID) *events.ApplicationCommandInteractionCreate {
	f.serial++
	e := interaction(f.t, name, true)
	body := map[string]any{"id": fmt.Sprint(100 + f.serial), "application_id": "2", "type": 2, "token": "test", "guild_id": guild.String(), "user": map[string]any{"id": user.String(), "username": "tester"}, "data": map[string]any{"id": "4", "name": name, "type": 1, "options": lifecycleOptions(name)}}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &e.ApplicationCommandInteraction); err != nil {
		f.t.Fatal(err)
	}
	return e
}
func (f *lifecycleFixture) request(name string) {
	f.t.Helper()
	f.r.mu.Lock()
	f.r.update = d.MessageUpdate{}
	f.r.response = ""
	f.r.mu.Unlock()
	e := f.interaction(name)
	acks := 0
	before := f.g.player.Ticket()
	e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
		acks++
		if f.g.player.Ticket() != before {
			f.t.Fatal("mutation before acknowledgement")
		}
		want := d.InteractionResponseTypeDeferredCreateMessage
		if name == "play" {
			want = d.InteractionResponseTypeCreateMessage
		}
		if typ != want {
			f.t.Fatalf("%s ack=%v want=%v", name, typ, want)
		}
		return nil
	}
	f.b.command(e)
	if acks != 1 {
		f.t.Fatalf("%s acknowledged %d times", name, acks)
	}
	f.model.command(f, name)
	synctest.Wait()
	f.check(name)
	f.r.mu.Lock()
	hasResponse := f.r.update.Content != nil || f.r.update.Embeds != nil || f.r.update.Components != nil
	f.r.mu.Unlock()
	if !hasResponse {
		f.t.Fatal("missing final Discord response", name)
	}
}
func (f *lifecycleFixture) eof() {
	f.t.Helper()
	if f.model.current == "" {
		return
	}
	if f.model.paused {
		f.request("resume")
	}
	old := f.model.current
	f.finish <- nil
	switch f.model.loop {
	case "track":
		f.model.queue = append([]string{old}, f.model.queue...)
	case "queue":
		f.model.queue = append(f.model.queue, old)
	}
	f.model.current = ""
	f.model.paused = false
	f.model.settle()
	synctest.Wait()
	f.check("natural EOF")
}

func (f *lifecycleFixture) play(ctx context.Context, _ media.Track, v player.Voice) error {
	if f.active.Add(1) != 1 {
		f.overlap.Store(true)
	}
	defer f.active.Add(-1)
	defer f.cleaned.Add(1)
	if err := v.WriteFrame(ctx, []byte{1}); err != nil {
		return err
	}
	select {
	case err := <-f.finish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
