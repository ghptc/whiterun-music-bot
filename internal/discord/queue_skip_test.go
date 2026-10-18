package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestQueuePagesExposeEveryPosition(t *testing.T) {
	for _, size := range []int{0, 1, 10, 11, 122, 123, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			state := player.State{Current: &media.Track{Title: "Current track"}, Paused: true}
			for i := range size {
				state.Queued = append(state.Queued, media.Track{Title: fmt.Sprintf("Song %04d", i+1)})
			}
			pages := max(1, (size+queuePageSize-1)/queuePageSize)
			for page := 1; page <= pages; page++ {
				text := queuePageText(state, page)
				if !strings.Contains(text, "Paused: Current track") || !strings.Contains(text, fmt.Sprintf("%d tracks remaining", size)) || !strings.Contains(text, fmt.Sprintf("%d total including current", size+1)) {
					t.Fatal(text)
				}
				if !strings.Contains(text, fmt.Sprintf("Page %d/%d", page, pages)) {
					t.Fatal("wrong page count")
				}
				for i := range size {
					want := i >= (page-1)*queuePageSize && i < page*queuePageSize
					if strings.Contains(text, fmt.Sprintf("%d. Song %04d\n", i+1, i+1)) != want {
						t.Fatalf("position %d on page %d", i+1, page)
					}
				}
				if len(utf16.Encode([]rune(text))) > 2000 {
					t.Fatal("Discord content limit exceeded")
				}
			}
			for _, page := range []int{0, -1, pages + 1, 1000000} {
				if !strings.Contains(queuePageText(state, page), "Queue has") {
					t.Fatal("invalid page accepted")
				}
			}
		})
	}
	if text := queuePageText(player.State{}, 1); !strings.Contains(text, "nothing playing") || !strings.Contains(text, "queue is empty") {
		t.Fatal(text)
	}
}

func TestQueueShowsProviderBlockedState(t *testing.T) {
	state := player.State{
		ProviderBlocked: true,
		ProviderReason:  "youtube_auth_challenge",
		Entries:         []player.QueueEntry{{Track: media.Track{Title: "Track two"}, Position: 2}},
	}
	text := queuePageText(state, 1)
	if !strings.Contains(text, "Playback blocked by YouTube") || !strings.Contains(text, "use /resume") || !strings.Contains(text, "1. Track two") {
		t.Fatal(text)
	}
}

func TestQueuePageWorstCaseDiscordSize(t *testing.T) {
	for _, count := range []int{123, 1000} {
		long := strings.Repeat("🎵", 200) + " @everyone\n**markup**"
		state := player.State{Current: &media.Track{Title: long, RequestedSource: "spotify"}}
		for range count {
			state.Queued = append(state.Queued, media.Track{Title: long, Artist: long})
		}
		for page := 1; page <= max(1, (count+9)/10); page++ {
			if text := queuePageText(state, page); len(utf16.Encode([]rune(text))) > 2000 {
				t.Fatalf("page %d too large: %d", page, len(utf16.Encode([]rune(text))))
			}
		}
	}
}
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
		if !ok || opt.Required || opt.MinValue == nil || *opt.MinValue != 1 || opt.MaxValue == nil {
			t.Fatal(opt)
		}
		if c.Name == "skip" && (opt.Name != "count" || *opt.MaxValue != 1001) {
			t.Fatal(opt)
		}
		if c.Name == "queue" && (opt.Name != "page" || *opt.MaxValue != 100) {
			t.Fatal(opt)
		}
	}
	if found != 2 {
		t.Fatal(found)
	}
}
func TestQueuePageAndBulkSkipCommands(t *testing.T) {
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
			b.command(intInteraction(t, "queue", "page", 13))
			if !strings.Contains(r.response, "121. Song 122") || !strings.Contains(r.response, "122. Song 123") || !strings.Contains(r.response, "123 total including current") {
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
	for _, page := range []int{0, 1, 2, 13} {
		e := interaction(t, "queue", true)
		if page > 0 {
			e = intInteraction(t, "queue", "page", page)
		}
		acknowledged := false
		e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
			message, ok := data.(d.MessageCreate)
			if typ != d.InteractionResponseTypeDeferredCreateMessage || (data != nil && (!ok || message.Flags&d.MessageFlagEphemeral != 0)) {
				t.Fatalf("queue is not public: %v %#v", typ, data)
			}
			acknowledged = true
			return nil
		}
		b.command(e)
		actualPage := max(1, page)
		if !acknowledged || !strings.Contains(r.response, "1/123 matched") || !strings.Contains(r.response, "122 tracks remaining") || !strings.Contains(r.response, fmt.Sprintf("Page %d/13", actualPage)) {
			t.Fatal(r.response)
		}
		for i := 1; i <= 122; i++ {
			want := i > (actualPage-1)*10 && i <= actualPage*10
			if strings.Contains(r.response, fmt.Sprintf("%d. Song %03d [resolving]\n", i, i+1)) != want {
				t.Fatalf("incorrect pending position %d: %s", i, r.response)
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

func TestPendingQueueWorstCaseDiscordSize(t *testing.T) {
	state := player.State{Current: &media.Track{Title: strings.Repeat("🎵", 200)}, Resolving: []player.CollectionProgress{{Total: 1000, Resolved: 1}}}
	for i := range 1000 {
		state.Entries = append(state.Entries, player.QueueEntry{Track: media.Track{Title: strings.Repeat("🎵", 200)}, Pending: true, Position: i + 1})
	}
	for page := 1; page <= 100; page++ {
		text := queuePageText(state, page)
		if len(utf16.Encode([]rune(text))) > 2000 || !strings.Contains(text, "1/1000 matched") {
			t.Fatalf("invalid pending page %d: %s", page, text)
		}
	}
}
