package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func TestRootsHelpCompleteStaticAndWithinLimits(t *testing.T) {
	embed := helpEmbed()
	if embed.Title != "Help" {
		t.Fatalf("help title: %q", embed.Title)
	}
	text := ""
	if len(embed.Fields) != 4 {
		t.Fatal(embed)
	}
	total := len(utf16.Encode([]rune(embed.Title)))
	for i, f := range embed.Fields {
		if f.Name != []string{"PLAYBACK", "QUEUE", "EXTRAS", "TAVERN"}[i] {
			t.Fatal(f.Name)
		}
		n := len(utf16.Encode([]rune(f.Value)))
		if n > 1024 {
			t.Fatal(n)
		}
		total += n + len(f.Name)
		text += f.Value + "\n"
	}
	if total > 6000 {
		t.Fatal(total)
	}
	registered := commands()
	if len(registered) != 22 {
		t.Fatal(len(registered))
	}
	seen := map[string]bool{}
	for _, c := range registered {
		name := c.CommandName()
		if seen[name] {
			t.Fatal(name)
		}
		seen[name] = true
		if strings.Count(text, "/"+name+" —") != 1 {
			t.Fatal(name)
		}
	}
	if strings.Count(text, "/") != len(registered) {
		t.Fatal("help contains unregistered commands")
	}
}
func TestExpandedPermissionsAndUnavailableProviders(t *testing.T) {
	b, r := testBot(t)
	for _, name := range []string{"remove", "move", "shuffle", "jump", "replay", "loop", "seek", "autoplay"} {
		b.command(interaction(t, name, true))
		if !strings.Contains(r.response, "Join a voice channel") {
			t.Fatal(name, r.response)
		}
	}
	b.command(interaction(t, "lyrics", true))
	if !strings.Contains(r.response, "not playing") {
		t.Fatal(r.response)
	}
	b.command(interaction(t, "history", true))
	if !strings.Contains(r.response, "SQLite") {
		t.Fatal(r.response)
	}
	e := interaction(t, "help", true)
	e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
		message, ok := data.(d.MessageCreate)
		if typ != d.InteractionResponseTypeDeferredCreateMessage || !ok || message.Flags&d.MessageFlagEphemeral == 0 {
			t.Fatal("help must be ephemeral")
		}
		return nil
	}
	b.command(e)
	if r.update.Embeds == nil || len(*r.update.Embeds) != 1 {
		t.Fatal("missing help embed")
	}
}

func TestExpandedHandlersWithPlayerAndSQLite(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	b, r := testBot(t)
	b.store = db
	g := b.getGuild(1)
	g.player.Close(context.Background())
	started := make(chan string, 10)
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, tr media.Track, _ player.Voice) error {
		started <- tr.Title
		<-ctx.Done()
		return ctx.Err()
	})
	if err := g.player.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	g.conn = &dummyConn{}
	g.channel = 5
	if err := g.player.Attach(transport{conn: g.conn}); err != nil {
		t.Fatal(err)
	}
	listener(b, 1, 3, false)
	request := func(name, options string) {
		t.Helper()
		e := interaction(t, name, true)
		raw := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"3","username":"tester"},"data":{"id":"4","name":%q,"type":1,"options":%s}}`, name, options)
		if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
			t.Fatal(err)
		}
		b.command(e)
	}
	wait := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatal(got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("stream transition timed out")
		}
	}
	if err := g.player.Enqueue(media.Track{Title: "current", Duration: time.Minute}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	wait("current")
	if err := g.player.EnqueueMany([]media.Track{{Title: "a"}, {Title: "b"}, {Title: "c"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	request("move", `[{"name":"from","type":4,"value":3},{"name":"to","type":4,"value":1}]`)
	if g.player.State().Entries[0].Track.Title != "c" {
		t.Fatal(r.response)
	}
	request("remove", `[{"name":"position","type":4,"value":2}]`)
	if g.player.Length() != 2 {
		t.Fatal(r.response)
	}
	request("shuffle", `[]`)
	if g.player.Length() != 2 {
		t.Fatal(r.response)
	}
	request("replay", `[]`)
	wait("current")
	request("loop", `[{"name":"mode","type":3,"value":"track"}]`)
	if g.player.State().Loop != "track" {
		t.Fatal(r.response)
	}
	request("seek", `[{"name":"position","type":3,"value":"0:30"}]`)
	if !strings.Contains(r.response, "unavailable") {
		t.Fatal(r.response)
	}
	request("autoplay", `[{"name":"enabled","type":5,"value":true}]`)
	if !g.player.State().Autoplay || !strings.Contains(r.response, "unavailable") {
		t.Fatal(r.response)
	}
	request("autoplay", `[{"name":"enabled","type":5,"value":false}]`)
	if g.player.State().Autoplay {
		t.Fatal(r.response)
	}
	request("lyrics", `[]`)
	if !strings.Contains(r.response, "licensed") {
		t.Fatal(r.response)
	}
	next := g.player.State().Entries[1].Track.Title
	request("jump", `[{"name":"position","type":4,"value":2}]`)
	wait(next)
	if g.player.Length() != 0 {
		t.Fatal("jump counted wrong")
	}
	if err := db.RecordPlayback(b.ctx, "1", media.Track{Title: strings.Repeat("🎵", 200), Artist: strings.Repeat("🎵", 200)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := db.RecordPlayback(b.ctx, "1", media.Track{Title: strings.Repeat("🎵", 200)}); err != nil {
			t.Fatal(err)
		}
	}
	before := g.player.State()
	request("history", `[{"name":"limit","type":4,"value":20}]`)
	if len(utf16.Encode([]rune(r.response))) > 2000 || !reflect.DeepEqual(before, g.player.State()) {
		t.Fatal("history changed state or exceeded message limit")
	}
}
