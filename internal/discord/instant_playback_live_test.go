package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/perfgate"
	"whiterun/internal/storage"
)

// Synthetic command stimulus at the production dispatcher boundary; actual
// gateway, Spotify, YouTube, FFmpeg, DAVE session and Discord UDP connection.
// Only the specified Voice channel is permitted; command registration is untouched.
type instantLiveRest struct{ rest.Rest }

func (r instantLiveRest) UpdateInteractionResponse(snowflake.ID, string, d.MessageUpdate, ...rest.RequestOpt) (*d.Message, error) {
	return &d.Message{}, nil
}

type instantLiveCase struct {
	Name, Kind, Query string
	Count             int
	Force             bool
}

func TestInstantPlaybackDiscordLive(t *testing.T) {
	if os.Getenv("BARD_DISCORD_LIVE") != "1" {
		t.Skip("requires explicit authorized guild/channel and Discord credentials")
	}
	guildID, err := snowflake.Parse(os.Getenv("BARD_LIVE_GUILD"))
	if err != nil {
		t.Fatal("authorized guild required")
	}
	channelID, err := snowflake.Parse(os.Getenv("BARD_LIVE_CHANNEL"))
	if err != nil {
		t.Fatal("authorized channel required")
	}
	appID, err := snowflake.Parse(os.Getenv("DISCORD_APPLICATION_ID"))
	if err != nil {
		t.Fatal("application ID required")
	}
	logs := &instantLog{traces: map[string]*instantTrace{}, eof: map[string]*instantTrace{}}
	b, err := New(context.Background(), os.Getenv("DISCORD_TOKEN"), appID, slog.New(instantHandler{shared: logs}))
	if err != nil {
		t.Fatal("Discord client initialization failed")
	}
	var db *storage.DB
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.Close(ctx)
		if db != nil {
			_ = db.Close()
		}
	}()
	db, err = storage.Open(filepath.Join(t.TempDir(), "guild-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	b.ConfigureStore(db)
	b.ConfigureSpotify(&media.Spotify{ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN"), Log: b.log})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	channel, err := b.client.Rest.GetChannel(channelID, rest.WithCtx(ctx))
	if err != nil {
		t.Fatal("cannot inspect authorized channel")
	}
	if channel.Type() != d.ChannelTypeGuildVoice && channel.Type() != d.ChannelTypeGuildStageVoice {
		t.Fatalf("authorized destination is not a Voice channel: type=%v", channel.Type())
	}
	if err = b.client.OpenGateway(ctx); err != nil {
		t.Fatal("Discord gateway connection failed")
	}
	b.client.Rest = instantLiveRest{b.client.Rest}
	b.client.Caches.AddMember(d.Member{GuildID: guildID, User: d.User{ID: 3}})
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: guildID, UserID: 3, ChannelID: &channelID})
	cases := []instantLiveCase{
		{"youtube-single", "youtube", "https://www.youtube.com/watch?v=MOIwxOYdhrk", 1, false},
		{"youtube-force", "youtube", "https://www.youtube.com/watch?v=MOIwxOYdhrk", 1, true},
		{"spotify-single", "track", "https://open.spotify.com/track/3bC1ahPIYt1btJzSSEyyrF", 1, false},
		{"spotify-album", "album", "https://open.spotify.com/album/7eLwoxxWs6lfkVYJGkGNbk", 10, false},
		{"playlist-10", "playlist", "https://open.spotify.com/playlist/3tfeDqgxoMDd5sCmefEtx8", 10, false},
		{"playlist-50", "playlist", "https://open.spotify.com/playlist/6dADHYLaAAQ61tUZJyIljF", 50, false},
		{"playlist-123", "playlist", "https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg", 123, false},
		{"playlist-302", "playlist", "https://open.spotify.com/playlist/6BL3uZVI7Q41TtpsLbmmLt", 302, false},
		{"playlist-500", "playlist", "https://open.spotify.com/playlist/0OdkLDK5wUdzAd2OT03hQz", 500, false},
	}
	if manifest := os.Getenv("BARD_LIVE_MANIFEST"); manifest != "" {
		raw, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
	}
	samples := 3
	if raw := os.Getenv("BARD_LIVE_SAMPLES"); raw != "" {
		samples, err = strconv.Atoi(raw)
		if err != nil || samples < 1 || samples > 100 {
			t.Fatal("live samples must be 1..100")
		}
	}
	var serial snowflake.ID = 1000
	for _, c := range cases {
		for _, mode := range []string{"cold", "warm"} {
			t.Run(c.Name+"/"+mode, func(t *testing.T) {
				var values []time.Duration
				var traces []map[string]int64
				var failures []string
				for repeat := 0; repeat < samples; repeat++ {
					g := b.getGuild(guildID)
					g.mu.Lock()
					g.searchCancel()
					g.searchCtx, g.searchCancel = context.WithTimeout(b.ctx, 75*time.Second)
					g.mu.Unlock()
					mediaDB, err := storage.Open(filepath.Join(t.TempDir(), "media.db"))
					if err != nil {
						t.Fatal(err)
					}
					b.resolver.YouTube.Mappings = mediaDB
					b.resolver.YouTube.Cache = &media.SearchCache{}
					if mode == "warm" {
						collection, progressive, err := b.resolver.PreparePlay(g.searchCtx, c.Query, c.Force)
						if err != nil {
							failures = append(failures, "provider metadata warmup failed: "+media.SafeDiagnostic([]byte(err.Error())))
							g.searchCancel()
							mediaDB.Close()
							continue
						}
						if progressive && len(collection.Tracks) > 0 {
							if _, err = b.resolver.MatchSpotify(g.searchCtx, collection.Tracks[0], true); err != nil {
								failures = append(failures, "provider match warmup failed: "+media.SafeDiagnostic([]byte(err.Error())))
								g.searchCancel()
								mediaDB.Close()
								continue
							}
						}
					}
					serial++
					e := instantInteraction(t, serial, guildID, "youtube", c.Force)
					data := e.SlashCommandInteractionData()
					option := data.Options["query"]
					encoded, _ := json.Marshal(c.Query)
					option.Value = encoded
					data.Options["query"] = option
					e.Data = data
					tr := newInstantTrace()
					logs.Lock()
					logs.traces[serial.String()] = tr
					logs.Unlock()
					commandDone := make(chan struct{})
					go func() { defer close(commandDone); b.commandAt(e, tr.start) }()
					timer := time.NewTimer(75 * time.Second)
					done := false
					select {
					case <-tr.done:
						done = true
					case <-commandDone:
						g := b.getGuild(guildID)
						state := g.player.State()
						if state.Current != nil || len(state.Entries) > 0 {
							select {
							case <-tr.done:
								done = true
							case <-timer.C:
							}
						}
					case <-timer.C:
					}
					timer.Stop()
					g = b.getGuild(guildID)
					if done {
						if _, ok := g.conn.(*voiceConn); !ok {
							t.Fatal("actual DAVE transport required")
						}
						sample := tr.snapshot()
						actual := int(sample["metadata_tracks"])
						if c.Kind != "youtube" && actual <= 0 {
							failures = append(failures, "missing source metadata track count")
						}
						if c.Kind != "youtube" && actual > 0 {
							if c.Count == 0 && actual >= 8 && actual <= 20 {
								c.Count = actual
							}
							if actual != c.Count {
								failures = append(failures, fmt.Sprintf("metadata count mismatch: got %d expected %d", actual, c.Count))
							}
						}
						values = append(values, time.Duration(sample["transport_sent"]))
						traces = append(traces, sample)
					} else {
						failures = append(failures, "no actual Discord frame; metadata/match/Voice stage failed or timed out")
					}
					g.searchCancel()
					g.player.Stop()
					<-commandDone
					g.searchCancel()
					mediaDB.Close()
				}
				budget := 5 * time.Second
				if mode == "warm" {
					budget = 2 * time.Second
				}
				status := "PASS"
				if len(failures) > 0 || perfgate.Check(values, budget) != nil {
					status = "FAIL"
				}
				raw, _ := json.Marshal(map[string]any{"scenario": instantScenario{Name: c.Name, Kind: c.Kind, Count: c.Count, Guilds: 1, Mode: mode}, "ttfa": perfgate.Summarize(values), "budget_ms": budget.Milliseconds(), "environment": "real-providers-discord-dave", "dave_verified": len(values) > 0, "status": status, "monotonic_ns": traces, "errors": failures, "ack_transport": "simulated", "stimulus": "synthetic interaction at commandAt; real Discord UDP"})
				t.Log("PERF_GATE_JSON " + string(raw))
			})
		}
	}
}

// Diagnose provider failures without opening Discord or producing audio.
func TestInstantPlaybackMetadataLive(t *testing.T) {
	if os.Getenv("BARD_PROVIDER_METADATA") != "1" {
		t.Skip("explicit provider metadata diagnostic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := &media.Spotify{ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN")}
	started := time.Now()
	collection, err := s.Collection(ctx, "playlist", "0OdkLDK5wUdzAd2OT03hQz")
	record := map[string]any{"scenario": "playlist-500-metadata-diagnostic", "elapsed_ms": float64(time.Since(started)) / float64(time.Millisecond), "tracks": len(collection.Tracks), "success": err == nil}
	if err != nil {
		record["error"] = media.SafeDiagnostic([]byte(err.Error()))
	}
	raw, _ := json.Marshal(record)
	t.Log("PERF_METADATA_JSON " + string(raw))
}
