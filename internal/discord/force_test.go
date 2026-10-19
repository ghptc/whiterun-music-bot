package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func forceInteraction(t *testing.T, flag string) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	e := qualityInteraction(t, "play", 3, "")
	extra := ""
	if flag != "" {
		extra = fmt.Sprintf(`,{"name":"force","type":5,"value":%s}`, flag)
	}
	body := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"3","username":"tester"},"data":{"id":"4","name":"play","type":1,"options":[{"name":"query","type":3,"value":"https://youtu.be/abcdefghijk?list=ignored&index=2"}%s]}}`, extra)
	if err := json.Unmarshal([]byte(body), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestForceCommandQueueAndControls(t *testing.T) {
	for _, flag := range []string{"", "false", "true"} {
		t.Run("force_"+flag, func(t *testing.T) {
			b, r := testBot(t)
			g := b.getGuild(1)
			g.player.Close(context.Background())
			started := make(chan media.Track, 4)
			g.player = player.New(b.ctx, b.log, func(ctx context.Context, tr media.Track, v player.Voice) error {
				v.WriteFrame(ctx, []byte{1})
				started <- tr
				<-ctx.Done()
				return ctx.Err()
			})
			if err := g.player.Attach(qualityVoice{}); err != nil {
				t.Fatal(err)
			}
			g.conn = &dummyConn{}
			g.channel = 5
			b.searches = make(chan struct{}, 4)
			b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
			yt := filepath.Join(t.TempDir(), "yt-dlp")
			if err := os.WriteFile(yt, []byte("#!/bin/sh\necho '{\"id\":\"abcdefghijk\",\"title\":\"Manual video\",\"duration\":17,\"thumbnail\":\"https://i.ytimg.com/vi/abcdefghijk/default.jpg\"}'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			b.resolver = media.CollectionResolver{YouTube: media.Resolver{Binary: yt}}
			if err := g.player.Enqueue(media.Track{Title: "first"}, g.player.Ticket()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("no first track")
			}
			b.command(forceInteraction(t, flag))
			if flag == "true" {
				if r.response != "Queued: Manual video — Direct YouTube" {
					t.Fatal(r.response)
				}
			} else if r.response != "Queued: Manual video" {
				t.Fatal(r.response)
			}
			state := g.player.State()
			if len(state.Queued) != 1 || state.Current.Title != "first" || state.Queued[0].ForcedYouTube != (flag == "true") || state.Queued[0].RequestedBy != "3" || state.Queued[0].YouTubeThumbnailURL == "" {
				t.Fatal(state)
			}
			b.command(qualityInteraction(t, "skip", 3, ""))
			select {
			case tr := <-started:
				if tr.VideoID != "abcdefghijk" {
					t.Fatal(tr)
				}
			case <-time.After(time.Second):
				t.Fatal("no forced track")
			}
			for _, cmd := range []string{"pause", "resume"} {
				b.command(qualityInteraction(t, cmd, 3, ""))
				if !strings.Contains(r.response, map[string]string{"pause": "paused", "resume": "resumed"}[cmd]) {
					t.Fatal(r.response)
				}
			}
			b.command(qualityInteraction(t, "skip", 3, ""))
			if g.player.State().Current != nil || g.player.Length() != 0 {
				t.Fatal("skip failed")
			}
		})
	}
}
func TestForceCommandRequiresSameChannel(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	g.conn = &dummyConn{}
	g.channel = 5
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(6))})
	b.command(forceInteraction(t, "true"))
	if !strings.Contains(r.response, "Join the Bard's voice channel") || g.player.Length() != 0 {
		t.Fatal(r.response)
	}
}
func TestForceSlashContract(t *testing.T) {
	for _, c := range commands() {
		s := c.(d.SlashCommandCreate)
		if s.Name != "play" {
			continue
		}
		n := 0
		for _, o := range s.Options {
			if b, ok := o.(d.ApplicationCommandOptionBool); ok && b.Name == "force" {
				n++
				if b.Required {
					t.Fatal("force required")
				}
			}
		}
		if n != 1 {
			t.Fatal("missing native boolean force")
		}
	}
}

// Verify the actual JSON contract used by both publication scopes. Discord
// treats an omitted `required` property as false (the SDK uses omitempty).
func TestForcePublishedJSONContract(t *testing.T) {
	for _, scope := range []snowflake.ID{0, 1} {
		t.Run(scope.String(), func(t *testing.T) {
			found := false
			for _, command := range scopedCommands(scope) {
				if command.CommandName() != "play" {
					continue
				}
				found = true
				payload, err := json.Marshal(command)
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Options []struct {
						Name     string `json:"name"`
						Type     int    `json:"type"`
						Required bool   `json:"required"`
					} `json:"options"`
				}
				if err := json.Unmarshal(payload, &wire); err != nil {
					t.Fatal(err)
				}
				forceCount := 0
				for _, option := range wire.Options {
					if option.Name == "query" && !option.Required {
						t.Fatal("query must remain required")
					}
					if option.Name == "force" {
						forceCount++
						if option.Type != 5 || option.Required {
							t.Fatalf("invalid force publication: %s", payload)
						}
					}
				}
				if forceCount != 1 {
					t.Fatalf("missing or duplicate force: %s", payload)
				}
			}
			if !found {
				t.Fatal("play not published")
			}
		})
	}
}

func TestForcedNowPlayingKeepsThumbnail(t *testing.T) {
	tr := media.Track{ForcedYouTube: true, RequestedSource: "youtube", Title: "Manual video", YouTubeThumbnailURL: "https://i.ytimg.com/vi/abcdefghijk/default.jpg"}
	m := playbackMessage(player.PlaybackEvent{Track: tr})
	if len(m.Components) != 1 || m.Components[0].(d.ContainerComponent).Components[1].(d.SectionComponent).Accessory.(d.ThumbnailComponent).Media.URL != tr.YouTubeThumbnailURL {
		t.Fatal(m)
	}
}

func TestForcedBotPlaybackFailureKeepsExactURL(t *testing.T) {
	b, _ := testBot(t)
	dir := t.TempDir()
	yt := filepath.Join(dir, "yt-dlp")
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + calls + "'\nexit 1\n"
	if err := os.WriteFile(yt, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b.resolver.YouTube = media.Resolver{Binary: "must-not-search"}
	b.streamer = player.Streamer{YTDLP: yt, FFmpeg: "ffmpeg"}
	tr := media.Track{ForcedYouTube: true, RequestedSource: "youtube", VideoID: "abcdefghijk", URL: "https://youtu.be/abcdefghijk?list=ignored", Title: "Interview", Duration: 17 * time.Second}
	if err := b.play(context.Background(), tr, qualityVoice{}); err == nil {
		t.Fatal("failure hidden")
	}
	args, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(args), "https://www.youtube.com/watch?v=abcdefghijk") != 1 || strings.Contains(string(args), "ytsearch") || strings.Contains(string(args), "list=ignored") {
		t.Fatal(string(args), err)
	}
}
