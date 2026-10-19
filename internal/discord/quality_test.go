package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func qualityInteraction(t *testing.T, name string, user int, query string) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	e := interaction(t, name, true)
	options := ""
	if name == "play" {
		q, _ := json.Marshal(query)
		options = `,"options":[{"name":"query","type":3,"value":` + string(q) + `}]`
	}
	body := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"%d","username":"tester"},"data":{"id":"4","name":"%s","type":1%s}}`, user, name, options)
	if err := json.Unmarshal([]byte(body), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
	return e
}
func TestChannelAuthorizationForEveryMutation(t *testing.T) {
	for _, name := range []string{"pause", "resume", "skip", "previous", "play", "clear", "stop", "leave"} {
		for _, channel := range []snowflake.ID{0, 6} {
			t.Run(fmt.Sprintf("%s/outside_%d", name, channel), func(t *testing.T) {
				b, r := testBot(t)
				g := b.getGuild(1)
				g.conn = &dummyConn{}
				g.channel = 5
				if channel != 0 {
					b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 4, ChannelID: &channel})
				}
				b.command(qualityInteraction(t, name, 4, "Song"))
				if !strings.Contains(r.response, "Join") {
					t.Fatal(r.response)
				}
				if g.conn == nil {
					t.Fatal("unauthorized leave")
				}
			})
		}
	}
}
func TestAnyChannelMemberControlsPlaybackAndAddsCollections(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	g.player.Close(context.Background())
	started := make(chan string, 20)
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, t media.Track, v player.Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		started <- t.Title
		<-ctx.Done()
		return ctx.Err()
	})
	// dummyConn has no UDP transport, so use a small frame-only Voice boundary.
	if err := g.player.Attach(qualityVoice{}); err != nil {
		t.Fatal(err)
	}
	g.conn = &dummyConn{}
	g.channel = 5
	for _, id := range []snowflake.ID{3, 4} {
		b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: id, ChannelID: new(snowflake.ID(5))})
	}
	if err := g.player.EnqueueMany([]media.Track{{Title: "one", RequestedBy: "3"}, {Title: "two", RequestedBy: "3"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatal(got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing start", want)
		}
	}
	receive("one")
	for _, tc := range []struct{ name, want string }{{"pause", "Playback paused"}, {"resume", "Playback resumed"}, {"now", "one"}, {"queue", "two"}, {"skip", "Skipped"}} {
		b.command(qualityInteraction(t, tc.name, 4, ""))
		response := r.response
		if tc.name == "queue" {
			response += queueAttachment(t, r.update, "queue.txt")
		}
		if !strings.Contains(response, tc.want) {
			t.Fatal(tc.name, r.response)
		}
	}
	receive("two")
	b.command(qualityInteraction(t, "previous", 4, ""))
	if !strings.Contains(r.response, "Previous song") {
		t.Fatal(r.response)
	}
	receive("one")
	b.resolver = media.CollectionResolver{Spotify: commandSpotify{t: t}, Search: func(context.Context, string) (media.Track, error) {
		return media.Track{URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
	}}
	b.searches = make(chan struct{}, 4)
	binary := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '{\"id\":\"abcdefghijk\",\"title\":\"Third\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b.resolver.YouTube = media.Resolver{Binary: binary}
	b.command(qualityInteraction(t, "play", 4, "https://www.youtube.com/watch?v=abcdefghijk"))
	if r.response != "Queued: Third" {
		t.Fatal(r.response)
	}
	// The same second user may also add a Spotify playlist.
	b.command(qualityInteraction(t, "play", 4, "https://open.spotify.com/playlist/37i9dQZF1DZ06evO1sJmec"))
	if !strings.Contains(r.response, "Queued:") {
		t.Fatal(r.response)
	}
	waitForCollections(t, g.player)
	for _, track := range g.player.State().Queued {
		if track.RequestedSource == "spotify" && track.RequestedBy != "4" {
			t.Fatal("requester lost", track)
		}
	}
}

type qualityVoice struct{}

func (qualityVoice) WriteFrame(context.Context, []byte) error { return nil }
func (qualityVoice) Speaking(context.Context, bool) error     { return nil }
func (qualityVoice) Close(context.Context)                    {}
func TestAnnouncementBackpressureDoesNotBlockAudio(t *testing.T) {
	b, _ := testBot(t)
	sent := make(chan struct{}, 1)
	p := player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, v player.Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		sent <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.Close(context.Background())
	g := &guild{player: p, announcements: make(chan announcement, 1)}
	g.announcements <- announcement{}
	g.announcementChannel.Store(5)
	b.bindAnnouncements(g)
	if err := p.Attach(qualityVoice{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(media.Track{Title: "Song", Artist: "Artist", RequestedBy: "4"}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("full announcement queue blocked audio")
	}
	text := playbackText(player.PlaybackEvent{Track: media.Track{Title: "Song", Artist: "Artist", RequestedBy: "4"}})
	if !strings.Contains(text, "<@4>") || !strings.Contains(text, "Artist") {
		t.Fatal(text)
	}
}

type failingMessageRest struct {
	rest.Rest
	attempts chan struct{}
}

func (r *failingMessageRest) CreateMessage(snowflake.ID, d.MessageCreate, ...rest.RequestOpt) (*d.Message, error) {
	r.attempts <- struct{}{}
	return nil, fmt.Errorf("controlled REST failure")
}
func TestAnnouncementRESTFailureDoesNotStopAudio(t *testing.T) {
	b, r := testBot(t)
	attempts := make(chan struct{}, 4)
	b.client.Rest = &failingMessageRest{Rest: r, attempts: attempts}
	frames := make(chan string, 4)
	release := make(chan struct{})
	g := announcementPlayer(t, b, func(ctx context.Context, track media.Track, v player.Voice) error {
		for range 2 {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
		}
		frames <- track.Title
		if track.Title == "two" {
			<-ctx.Done()
			return ctx.Err()
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := g.player.EnqueueMany([]media.Track{{Title: "one"}, {Title: "two"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempts:
	case <-time.After(time.Second):
		t.Fatal("REST not called")
	}
	close(release)
	for _, want := range []string{"one", "two"} {
		select {
		case got := <-frames:
			if got != want {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("REST error stopped audio")
		}
	}
	select {
	case <-attempts:
	case <-time.After(time.Second):
		t.Fatal("second announcement absent")
	}
	select {
	case <-attempts:
		t.Fatal("duplicate announcement")
	default:
	}
}
func TestQueueReportsImportOutcomes(t *testing.T) {
	state := player.State{Imports: []player.CollectionProgress{{Total: 6, Resolved: 2, Failed: 3, Uncertain: 1, MetadataSkipped: 1, Skipped: 1}}}
	v, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := v.Content
	for _, part := range []string{"2 matched", "1 uncertain", "2 rejected", "1 unavailable", "1 skipped"} {
		if !strings.Contains(text, part) {
			t.Fatal(text)
		}
	}
}
