package discord

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

// Opt-in only. A distinct test application prevents a second gateway from
// taking ownership of the production bot. No production store is ever opened.
func TestLocalAudioDiscordLive(t *testing.T) {
	if os.Getenv("BARD_LOCAL_AUDIO_LIVE") != "I_AUTHORIZE_TEST_BOT_AUDIO" {
		t.Skip("explicit real Voice authorization required")
	}
	parse := func(key string) snowflake.ID {
		id, err := snowflake.Parse(os.Getenv(key))
		if err != nil || id == 0 {
			t.Fatalf("invalid %s", key)
		}
		return id
	}
	app := parse("BARD_TEST_APPLICATION_ID")
	production := parse("BARD_PRODUCTION_APPLICATION_ID")
	if app == production {
		t.Fatal("test application must differ from production")
	}
	guildID := parse("BARD_TEST_GUILD_ID")
	channelID := parse("BARD_TEST_VOICE_CHANNEL_ID")
	root := os.Getenv("BARD_TEST_AUDIO_DIR")
	if !filepath.IsAbs(root) {
		t.Fatal("test audio root must be absolute")
	}
	token := os.Getenv("BARD_TEST_DISCORD_TOKEN")
	if token == "" {
		t.Fatal("test bot token required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	evidence := &localLiveEvidence{}
	log := slog.New(slog.NewJSONHandler(evidence, nil))
	b, err := New(ctx, token, app, log)
	if err != nil {
		t.Fatal("test bot initialization failed")
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		b.Close(cleanup)
	}()
	if err := b.ConfigureLocalAudio(root); err != nil {
		t.Fatal(err)
	}
	// Opening the gateway directly avoids changing slash command registrations.
	if err := b.client.OpenGateway(ctx); err != nil {
		t.Fatal("test gateway failed")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, ok := b.client.Caches.Guild(guildID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test guild cache unavailable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state, ok := b.client.Caches.VoiceState(guildID, b.client.ApplicationID); ok && state.ChannelID != nil {
		t.Fatal("test bot already has an active Voice session")
	}
	g := b.getGuild(guildID)
	if g == nil {
		t.Fatal("test guild unavailable")
	}
	g.mu.Lock()
	state := g.player.State()
	if state.Current != nil || len(state.Entries) > 0 || len(state.Resolving) > 0 || g.conn != nil {
		g.mu.Unlock()
		t.Fatal("test player busy; refusing to interrupt")
	}
	conn := b.client.VoiceManager.CreateConn(guildID)
	if err := conn.Open(ctx, channelID, false, true); err != nil {
		g.mu.Unlock()
		t.Fatal("test Voice join failed")
	}
	if err := g.player.Attach(b.playbackTransport(guildID, g, conn)); err != nil {
		conn.Close(player.VoiceCloseContext(ctx, "local_validation_attach_failed"))
		g.mu.Unlock()
		t.Fatal(err)
	}
	g.conn = conn
	g.channel = channelID
	tr, err := media.LocalTrack("validation.flac", 30*time.Second)
	if err == nil {
		tr.Timing = media.NewTiming(log.With("validation", "local_audio_ec2", "guild_id", guildID))
		err = g.player.Enqueue(tr, g.player.Ticket())
	}
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("validation_started_at=%s guild=%s channel=%s", time.Now().UTC().Format(time.RFC3339Nano), guildID, channelID)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		completed, failed, first, udp, progress := evidence.snapshot()
		if failed {
			t.Fatal("local audio failed; inspect correlated events")
		}
		if completed {
			if !first || !udp || progress < 30000 {
				t.Fatal("completion without UDP submission/frame/progress")
			}
			if g.player.State().Current != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			t.Logf("validation_finished_at=%s progress_ms=%d; human audibility confirmation still required", time.Now().UTC().Format(time.RFC3339Nano), progress)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(errors.New("real Voice validation timed out"))
		case <-ticker.C:
		}
	}
}

type localLiveEvidence struct {
	mu                            sync.Mutex
	completed, failed, first, udp bool
	progress                      int64
}

func (w *localLiveEvidence) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var event map[string]any
	if json.Unmarshal(p, &event) == nil {
		switch event["msg"] {
		case "first_discord_send":
			w.udp = true
		case "local_audio_first_frame":
			w.first = true
		case "local_audio_failed":
			w.failed = true
		case "local_audio_completed":
			w.completed = true
			if n, ok := event["progress_ms"].(float64); ok {
				w.progress = int64(n)
			}
		}
		// SDK diagnostics aren't exported: only safe local source events.
		if event["msg"] == "local_audio_opened" || event["msg"] == "local_audio_first_frame" || event["msg"] == "local_audio_completed" || event["msg"] == "local_audio_failed" || event["msg"] == "first_discord_send" {
			_, _ = os.Stdout.Write(p)
		}
	}
	return len(p), nil
}
func (w *localLiveEvidence) snapshot() (bool, bool, bool, bool, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.completed, w.failed, w.first, w.udp, w.progress
}
