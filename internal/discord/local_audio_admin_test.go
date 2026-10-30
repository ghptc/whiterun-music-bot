package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type localAdminREST struct {
	*recordingRest
	owner        snowflake.ID
	channelGuild snowflake.ID
	created      int
}

func (r *localAdminREST) GetCurrentApplication(...rest.RequestOpt) (*d.Application, error) {
	return &d.Application{Owner: &d.User{ID: r.owner}}, nil
}
func (r *localAdminREST) GetChannel(id snowflake.ID, _ ...rest.RequestOpt) (d.Channel, error) {
	var c d.GuildVoiceChannel
	err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":"%s","guild_id":"%s","type":2,"name":"validation"}`, id, r.channelGuild)), &c)
	return c, err
}
func (r *localAdminREST) CreateGuildCommand(_ snowflake.ID, guild snowflake.ID, command d.ApplicationCommandCreate, _ ...rest.RequestOpt) (d.ApplicationCommand, error) {
	if command.CommandName() != "audio-check" || guild != 1 {
		return nil, errors.New("wrong destination")
	}
	c := command.(d.SlashCommandCreate)
	if len(c.Options) != 0 {
		return nil, errors.New("arbitrary file option")
	}
	r.created++
	var result d.SlashCommand
	err := json.Unmarshal([]byte(`{"id":"123","application_id":"2","guild_id":"1","type":1,"name":"audio-check","description":"fixture","version":"1"}`), &result)
	return result, err
}

func TestLocalAudioAdminAuthorizationAndQueuePreservation(t *testing.T) {
	for _, scenario := range []string{"disabled", "nonowner", "wrongguild", "wrongchannel", "queue", "active", "loop", "autoplay", "cachedsession"} {
		t.Run(scenario, func(t *testing.T) {
			b, _ := testBot(t)
			b.localAdmin = localAudioAdmin{guild: 1, channel: 5, owner: 3}
			b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
			g := b.getGuild(1)
			switch scenario {
			case "disabled":
				b.localAdmin.owner = 0
			case "nonowner":
				b.localAdmin.owner = 4
			case "wrongguild":
				b.localAdmin.guild = 2
			case "wrongchannel":
				b.localAdmin.channel = 6
			case "queue":
				if err := g.player.Attach(qualityVoice{}); err != nil {
					t.Fatal(err)
				}
				if _, err := g.player.SetPaused(true); err == nil {
					t.Fatal("unexpected current")
				}
				// A durable queue can be pending with no Voice attachment after restore.
				g.player.Close(context.Background())
				b.store = &lifecycleStore{states: map[string]player.DurableState{"1": {Queue: []media.Track{{Title: "preserve", RequestedSource: "spotify"}}}}}
				delete(b.guilds, 1)
				g = b.getGuild(1)
			case "active":
				g.conn = &dummyConn{}
			case "loop":
				if err := g.player.SetLoop("queue"); err != nil {
					t.Fatal(err)
				}
			case "autoplay":
				g.player.SetAutoplay(true)
			case "cachedsession":
				b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 2, ChannelID: new(snowflake.ID(5))})
			}
			before := g.player.State()
			result := b.adminAudioCheck(interaction(t, "audio-check", true))
			if strings.Contains(result, "Starting") {
				t.Fatal("unexpected execution", scenario)
			}
			after := g.player.State()
			if len(before.Entries) != len(after.Entries) || before.Current != nil || after.Current != nil {
				t.Fatal("queue/session changed")
			}
		})
	}
}

func TestLocalAudioAdminManualRequestUsesExistingPlayer(t *testing.T) {
	b, r := testBot(t)
	b.localAdmin = localAudioAdmin{guild: 1, channel: 5, owner: 3}
	root, err := filepath.Abs("../player/testdata/local-audio")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ConfigureLocalAudio(root); err != nil {
		t.Fatal(err)
	}
	b.streamer.FFmpeg = "ffmpeg"
	b.streamer.YTDLP = "/never-execute"
	b.client.VoiceManager = lifecycleManager{}
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
	b.command(interaction(t, "audio-check", true))
	if !strings.Contains(r.queueText(), "Starting the fixed") {
		t.Fatal(r.queueText())
	}
	g := b.getGuild(1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		conn := g.conn.(*lifecycleConn)
		g.mu.Unlock()
		if conn.udp.frames.Load() > 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no synthetic UDP frames")
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := g.player.State()
	if snapshot.Current == nil || snapshot.Current.RequestedSource != "local" || snapshot.Current.URL != "validation.flac" {
		t.Fatal(snapshot)
	}
	if result := b.adminAudioCheck(interaction(t, "audio-check", true)); !strings.Contains(result, "refused") {
		t.Fatal("duplicate request admitted")
	}
	g.player.Stop()
}

func TestLocalAudioAdminRegistrationNeverPlays(t *testing.T) {
	for _, scenario := range []string{"ready", "noowner", "wrongdestination"} {
		t.Run(scenario, func(t *testing.T) {
			b, r := testBot(t)
			b.localAdmin = localAudioAdmin{guild: 1, channel: 5}
			mock := &localAdminREST{recordingRest: r, owner: 3, channelGuild: 1}
			b.client.Rest = mock
			if scenario == "noowner" {
				mock.owner = 0
			}
			if scenario == "wrongdestination" {
				mock.channelGuild = 2
			}
			err := b.syncLocalAudioAdmin(context.Background())
			if scenario == "ready" {
				if err != nil || mock.created != 1 || b.localAdmin.owner != 3 {
					t.Fatal(err, mock.created)
				}
			} else if err == nil || mock.created != 0 {
				t.Fatal("unsafe registration")
			}
			if len(b.guilds) != 0 {
				t.Fatal("startup created a playback worker")
			}
		})
	}
}
