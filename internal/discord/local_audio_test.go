package discord

import (
	"context"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whiterun/internal/media"
)

func TestLocalAudioBypassesProviders(t *testing.T) {
	b := &Bot{}
	if err := b.ConfigureLocalAudio("relative"); err == nil {
		t.Fatal("relative root accepted")
	}
	if err := b.ConfigureLocalAudio(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := b.ConfigureLocalAudio(""); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../player/testdata/local-audio")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ConfigureLocalAudio(root); err != nil {
		t.Fatal(err)
	}
	b.streamer.FFmpeg = "ffmpeg"
	b.streamer.YTDLP = "/must-never-run"
	tr, err := media.LocalTrack("validation.flac", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Even these fields must not route a local descriptor through a resolver.
	tr.VerifyBeforePlay = true
	tr.ForcedYouTube = true
	tr.Timing = media.NewTiming(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	tr.ResumeOffset = 29800 * time.Millisecond
	if err := b.play(context.Background(), tr, qualityVoice{}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAudioSlashControls(t *testing.T) {
	b, r := testBot(t)
	root, err := filepath.Abs("../player/testdata/local-audio")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ConfigureLocalAudio(root); err != nil {
		t.Fatal(err)
	}
	b.streamer.FFmpeg = "ffmpeg"
	b.streamer.YTDLP = "/must-never-run"
	g := b.getGuild(1)
	v := &integrityVoice{}
	if err := g.player.Attach(v); err != nil {
		t.Fatal(err)
	}
	conn := &dummyConn{}
	g.mu.Lock()
	g.conn = conn
	g.channel = 5
	g.mu.Unlock()
	b.client.Caches.AddVoiceState(discord.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))})
	tr, err := media.LocalTrack("validation.flac", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.player.EnqueueMany([]media.Track{tr, tr, tr}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("local command transition timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(func() bool { return v.frames.Load() > 3 })
	b.command(interaction(t, "pause", true))
	if !strings.Contains(r.queueText(), "paused") || !g.player.State().Paused {
		t.Fatal(r.queueText())
	}
	time.Sleep(50 * time.Millisecond)
	count := v.frames.Load()
	time.Sleep(100 * time.Millisecond)
	if v.frames.Load() != count || len(g.player.State().Queued) != 2 {
		t.Fatal("pause changed audio or queue")
	}
	b.command(interaction(t, "resume", true))
	if !strings.Contains(r.queueText(), "resumed") {
		t.Fatal(r.queueText())
	}
	wait(func() bool { return v.frames.Load() > count+2 })
	b.command(interaction(t, "skip", true))
	wait(func() bool { state := g.player.State(); return state.Current != nil && len(state.Queued) == 1 })
	b.command(interaction(t, "stop", true))
	wait(func() bool { state := g.player.State(); return state.Current == nil && len(state.Queued) == 0 })
	if !strings.Contains(r.queueText(), "queue is clear") || conn.closed {
		t.Fatal("stop response or unexpected Voice Leave")
	}
}
