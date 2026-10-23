package discord

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

// Keep the real gateway handlers and caches, but never handle interactions in
// this temporary test client. The deployed command handler remains the owner.
type soakEvents struct{ bot.EventManager }

func (e soakEvents) DispatchEvent(event bot.Event) {
	if _, ok := event.(*events.ApplicationCommandInteractionCreate); ok {
		return
	}
	e.EventManager.DispatchEvent(event)
}

// Explicit opt-in only. This is an integration test client, not a deployment.
func TestFIX025RealVoiceSoak(t *testing.T) {
	if os.Getenv("BARD_FIX025_SOAK") != "1" {
		t.Skip("requires explicitly authorized test channel and live credentials")
	}
	const guildID snowflake.ID = 404344088691867649
	const channelID snowflake.ID = 921489232374140978
	if os.Getenv("BARD_LIVE_GUILD") != guildID.String() || os.Getenv("BARD_LIVE_CHANNEL") != channelID.String() {
		t.Fatal("only the explicitly authorized test channel is allowed")
	}
	duration := 3*time.Hour + 5*time.Minute
	if raw := os.Getenv("BARD_SOAK_DURATION"); raw != "" {
		var err error
		duration, err = time.ParseDuration(raw)
		if err != nil || duration < 3*time.Hour {
			t.Fatal("soak duration must be at least three hours")
		}
	}
	appID, err := snowflake.Parse(os.Getenv("DISCORD_APPLICATION_ID"))
	if err != nil {
		t.Fatal("application ID required")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	b, err := New(context.Background(), os.Getenv("DISCORD_TOKEN"), appID, log)
	if err != nil {
		t.Fatal("client initialization failed")
	}
	b.client.EventManager = soakEvents{b.client.EventManager}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.Close(ctx)
	}()
	db, err := storage.Open(filepath.Join(t.TempDir(), "soak-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b.ConfigureStore(db)
	b.ConfigureSpotify(&media.Spotify{ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN"), Market: os.Getenv("SPOTIFY_MARKET"), Log: log})
	ctx, cancel := context.WithTimeout(b.ctx, 45*time.Second)
	defer cancel()
	channel, err := b.client.Rest.GetChannel(channelID)
	if err != nil || channel.Type() != d.ChannelTypeGuildVoice {
		t.Fatal("authorized Voice channel unavailable")
	}
	if err = b.client.OpenGateway(ctx); err != nil {
		t.Fatal("gateway open failed")
	}
	// Guild caches may arrive just after Ready. Never join over another session.
	ready := time.NewTicker(100 * time.Millisecond)
	defer ready.Stop()
	for {
		if _, ok := b.client.Caches.Guild(guildID); ok {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("guild cache unavailable")
		case <-ready.C:
		}
	}
	if state, known := b.client.Caches.VoiceState(guildID, b.client.ID()); known && state.ChannelID != nil {
		t.Fatal("bot already has a Voice session in test guild; refusing a duplicate")
	}
	g := b.getGuild(guildID)
	if g == nil {
		t.Fatal("player unavailable")
	}
	g.mu.Lock()
	g.channel = channelID
	if !b.hasListeners(guildID, g) {
		g.channel = 0
		g.mu.Unlock()
		t.Fatal("authorized channel has no human listeners; empty-channel policy prevents a three-hour soak")
	}
	conn := b.client.VoiceManager.CreateConn(guildID)
	g.conn = conn
	err = conn.Open(ctx, channelID, false, true)
	if err == nil {
		err = confirmVoiceReady(ctx, conn)
	}
	if err == nil {
		err = g.player.Attach(b.playbackTransport(guildID, g, conn))
	}
	g.mu.Unlock()
	if err != nil {
		t.Fatal("Voice session open/readiness failed", err)
	}
	b.workers.Add(1)
	go b.watchVoice()
	var mu sync.Mutex
	starts, resumes, unavailable, notices := 0, 0, 0, 0
	g.player.SetOnStart(func(e player.PlaybackEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Resumed {
			resumes++
		} else {
			starts++
		}
	})
	g.player.SetOnSkip(func(e player.PlaybackEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.FailureStage == "voice" {
			notices++
		} else {
			unavailable++
		}
	})
	collection, progressive, err := b.resolver.PreparePlay(b.ctx, "https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg", false)
	if err != nil || !progressive || len(collection.Tracks) != 123 {
		t.Fatal("original 123-position manifest unavailable", err)
	}
	_, err = g.player.BeginCollection(b.ctx, collection, b.resolver.MatchSpotify, g.player.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	connected := time.Now()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			mu.Lock()
			summary := map[string]any{"event": "fix025_soak_completed", "connected_ms": time.Since(connected).Milliseconds(), "starts": starts, "resumes": resumes, "unavailable": unavailable, "incident_notices": notices, "messages_sent": 0, "queue": g.player.State(), "manifest_positions": 123, "collection": g.player.State().Imports}
			mu.Unlock()
			raw, _ := json.Marshal(summary)
			t.Log(string(raw))
			return
		case <-tick.C:
			g.mu.Lock()
			attached := g.conn != nil
			g.mu.Unlock()
			if !attached {
				t.Fatal("Voice session ended before soak deadline (check empty-channel/user removal logs)")
			}
			log.Info("fix025_soak_sample", "connected_ms", time.Since(connected).Milliseconds(), "goroutines", runtime.NumGoroutine(), "health", g.player.Health(), "positions_remaining", g.player.Length())
		}
	}
}
