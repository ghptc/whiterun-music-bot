package discord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type announcementVoice struct{}

func (announcementVoice) WriteFrame(context.Context, []byte) error { return nil }
func (announcementVoice) Speaking(context.Context, bool) error     { return nil }
func (announcementVoice) Close(context.Context)                    {}
func announcementPlayer(t *testing.T, b *Bot, play player.PlayFunc) *guild {
	t.Helper()
	g := b.getGuild(1)
	g.player.Close(context.Background())
	g.player = player.New(b.ctx, b.log, play)
	b.bindAnnouncements(g)
	if err := g.player.Attach(announcementVoice{}); err != nil {
		t.Fatal(err)
	}
	g.announcementChannel.Store(9)
	return g
}
func nextAnnouncement(t *testing.T, r *recordingRest) sentMessage {
	t.Helper()
	select {
	case m := <-r.messages:
		m.message.Content = messageText(m.message)
		if m.message.Flags&d.MessageFlagEphemeral != 0 || m.message.AllowedMentions == nil || !strings.Contains(m.message.Content, "Now Playing") {
			t.Fatal(m)
		}
		return m
	case <-time.After(time.Second):
		t.Fatal("announcement missing")
		return sentMessage{}
	}
}
func TestAnnouncementsExactlyOnceOnEOFAndSkip(t *testing.T) {
	b, r := testBot(t)
	eof := make(chan struct{}, 3)
	g := announcementPlayer(t, b, func(ctx context.Context, _ media.Track, v player.Voice) error {
		for range 3 {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
		}
		select {
		case <-eof:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := g.player.EnqueueMany([]media.Track{{Title: "First"}, {Title: "Second"}, {Title: "Third", Live: true}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	first := nextAnnouncement(t, r)
	if first.channel != 9 || !strings.Contains(first.message.Content, "First") || !strings.Contains(first.message.Content, "Up next: Second") {
		t.Fatal(first)
	}
	g.announcementChannel.Store(10)
	eof <- struct{}{}
	second := nextAnnouncement(t, r)
	if second.channel != 10 || !strings.Contains(second.message.Content, "Second") || !strings.Contains(second.message.Content, "Up next: Third") {
		t.Fatal(second)
	}
	g.player.SkipN(1)
	third := nextAnnouncement(t, r)
	if !strings.Contains(third.message.Content, "Third") || strings.Contains(third.message.Content, "YouTube Live") || strings.Contains(third.message.Content, "Up next:") {
		t.Fatal(third)
	}
	g.player.Stop()
	select {
	case m := <-r.messages:
		t.Fatal("duplicate announcement", m)
	case <-time.After(20 * time.Millisecond):
	}
}
func TestAnnouncementPendingNextAndNoResolutionSpam(t *testing.T) {
	b, r := testBot(t)
	gate := make(chan struct{})
	g := announcementPlayer(t, b, func(ctx context.Context, _ media.Track, v player.Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	job, err := g.player.BeginCollection(b.ctx, media.Collection{Tracks: []media.Track{{Title: "First"}, {Title: "Second"}, {Title: "Third"}}}, func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if !first {
			select {
			case <-gate:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
		}
		track.URL = "https://youtube.com/watch?v=abcdefghijk"
		track.RequestedSource = "spotify"
		return track, nil
	}, g.player.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	message := nextAnnouncement(t, r).message.Content
	if !strings.Contains(message, "Up next: Second") || strings.Contains(message, "resolving") || strings.Contains(message, "Spotify metadata") {
		t.Fatal(message)
	}
	close(gate)
	select {
	case <-job.Done:
	case <-time.After(time.Second):
		t.Fatal("resolution blocked")
	}
	select {
	case m := <-r.messages:
		t.Fatal("resolution emitted announcement", m)
	default:
	}
	g.player.SkipN(2)
	message = nextAnnouncement(t, r).message.Content
	if !strings.Contains(message, "Third") || strings.Contains(message, "3/3") {
		t.Fatal(message)
	}
}

type announcementSpotify struct{}

func (announcementSpotify) Collection(context.Context, string, string) (media.Collection, error) {
	return media.Collection{Tracks: []media.Track{{Title: "Spotify song", Artist: "Artist"}, {Title: "Next song", Artist: "Artist"}}}, nil
}
func TestSuccessfulPlayPublicForEachSource(t *testing.T) {
	for _, tc := range []struct{ name, query, metadata string }{
		{"search", "Artist Song", `{"entries":[{"id":"abcdefghijk","title":"Artist Song Official Audio","channel":"Artist","duration":210,"categories":["Music"]}]}`},
		{"direct", "https://youtu.be/abcdefghijk", `{"id":"abcdefghijk","title":"Exact unofficial video"}`},
		{"live", "https://youtube.com/live/abcdefghijk", `{"id":"abcdefghijk","title":"Live now","is_live":true}`},
		{"playlist", "https://youtube.com/playlist?list=PLtest", `{"entries":[{"id":"abcdefghijk","title":"First"},{"id":"lmnopqrstuv","title":"Second"}]}`},
		{"spotify collection", "https://open.spotify.com/playlist/1234567890123456789012", `{}`},
		{"spotify track", "https://open.spotify.com/track/1234567890123456789012", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r := testBot(t)
			g := announcementPlayer(t, b, func(ctx context.Context, _ media.Track, v player.Voice) error {
				if err := v.WriteFrame(ctx, []byte{1}); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			})
			g.conn = &dummyConn{}
			g.channel = 5
			b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
			binary := filepath.Join(t.TempDir(), "yt-dlp")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat <<'JSON'\n"+tc.metadata+"\nJSON\n"), 0700); err != nil {
				t.Fatal(err)
			}
			b.searches = make(chan struct{}, 4)
			b.resolver = media.CollectionResolver{YouTube: media.Resolver{Binary: binary}, Spotify: announcementSpotify{}, Search: func(context.Context, string) (media.Track, error) {
				return media.Track{URL: "https://youtube.com/watch?v=abcdefghijk"}, nil
			}}
			e := interaction(t, "play", true)
			raw := `{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","channel":{"id":"99","type":0,"name":"music"},"user":{"id":"3","username":"tester"},"data":{"id":"4","name":"play","type":1,"options":[{"name":"query","type":3,"value":"` + tc.query + `"}]}}`
			if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
				t.Fatal(err)
			}
			ack := false
			e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, _ ...rest.RequestOpt) error {
				message, ok := data.(d.MessageCreate)
				if typ != d.InteractionResponseTypeCreateMessage || !ok || message.Flags&d.MessageFlagEphemeral != 0 {
					t.Fatalf("play not public: %#v", data)
				}
				ack = true
				return nil
			}
			b.command(e)
			if !ack || !(strings.HasPrefix(r.response, "Queued:") || strings.HasPrefix(r.response, "Added ")) {
				t.Fatal(r.response)
			}
			if g.announcementChannel.Load() != 99 {
				t.Fatal("play did not remember channel")
			}
			if m := nextAnnouncement(t, r); m.channel != 99 {
				t.Fatal(m)
			}
		})
	}
}

func TestSkippedAnnouncementKeepsIdentityAndPosition(t *testing.T) {
	text := playbackText(player.PlaybackEvent{Track: media.Track{Title: "Bring The Noise Remix - Pumpkin Edit", Artist: "Public Enemy", Position: 7, Total: 20}, SkippedReason: "External provider blocked access to audio."})
	for _, want := range []string{"Public Enemy", "Pumpkin Edit"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if strings.Contains(text, "Now Playing") {
		t.Fatal(text)
	}
	state := player.State{Current: &media.Track{Title: "Waiting"}}
	if strings.Contains(componentText([]d.LayoutComponent{nowContainer(state)}), "-# Playing") {
		t.Fatal("announced before first frame")
	}
	state.CurrentStarted = true
	if !strings.Contains(componentText([]d.LayoutComponent{nowContainer(state)}), "-# Playing") {
		t.Fatal("missing confirmed playback")
	}
}

func TestFailureAnnouncementsDistinguishResolutionAndInterruptedPlayback(t *testing.T) {
	for _, tc := range []struct{ stage, label string }{{"resolution", "Unavailable:"}, {"starting", "Playback failed to start:"}, {"playing", "Playback interrupted:"}} {
		event := player.PlaybackEvent{Track: media.Track{SpotifyID: "original-id", Title: "Little Bad Girl", Position: 9, Total: 123}, FailureStage: tc.stage, SkippedReason: "injected"}
		text := playbackText(event)
		if !strings.Contains(text, tc.label) || strings.Contains(text, "Skipped:") || strings.Contains(text, "original-id") || strings.Contains(text, "9/123") || strings.Contains(text, "injected") {
			t.Fatal(text)
		}
	}
}
