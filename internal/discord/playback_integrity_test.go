package discord

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type integrityVoice struct {
	qualityVoice
	frames atomic.Int32
}

func (v *integrityVoice) WriteFrame(context.Context, []byte) error { v.frames.Add(1); return nil }

// This crosses Bot.play, the production identity guard, process orchestration,
// actual FFmpeg/libopus and the Voice boundary. Only provider/Discord I/O is local.
func TestBotPlaybackRejectsWrongRecordingBeforeStreaming(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("FFmpeg required:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	metadata := filepath.Join(dir, "candidate.json")
	yt := filepath.Join(dir, "yt-dlp")
	if out, err := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "0.1", "-y", wav).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	script := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = '--dump-single-json' ]; then cat '" + metadata + "'; exit 0; fi\ndone\ncat '" + wav + "'\n"
	if err := os.WriteFile(yt, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	writeMetadata := func(title string) {
		t.Helper()
		body := `{"id":"abcdefghijk","title":"` + title + `","channel":"Artist - Topic","duration":210}`
		if err := os.WriteFile(metadata, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := testBot(t)
	b.resolver.YouTube = media.Resolver{Binary: yt}
	b.streamer = player.Streamer{YTDLP: yt, FFmpeg: ff}
	track := media.Track{SpotifyID: "id", RequestedSource: "spotify", Title: "Song", Artist: "Artist", Duration: 210 * time.Second, VideoID: "abcdefghijk", URL: "https://www.youtube.com/watch?v=abcdefghijk"}
	v := &integrityVoice{}
	writeMetadata("Song Live")
	if err := b.play(ctx, track, v); !errors.Is(err, media.ErrSpotifyMatch) {
		t.Fatal("wrong recording passed guard", err)
	}
	if v.frames.Load() != 0 {
		t.Fatal("incompatible recording delivered audio")
	}
	writeMetadata("Song")
	if err := b.play(ctx, track, v); err != nil {
		t.Fatal(err)
	}
	if v.frames.Load() <= 5 {
		t.Fatal("no media frames beyond end silence", v.frames.Load())
	}
}

func TestBotRestoresGuildSettingsAndMetadataWithoutVoice(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SaveGuild(ctx, "1", player.DurableState{Queue: []media.Track{{Title: "Recovered", SpotifyID: "original"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(ctx, "1", map[string]string{"announcement_channel": "99"}); err != nil {
		t.Fatal(err)
	}
	b, _ := testBot(t)
	b.ConfigureStore(db)
	// Close explicitly before the real DB, while testBot's cleanup stays idempotent.
	defer b.Close(ctx)
	if err := b.ConfigureMatchPolicy(media.DefaultMatchPolicy()); err != nil {
		t.Fatal(err)
	}
	g := b.getGuild(1)
	if g == nil || g.conn != nil || g.announcementChannel.Load() != 99 || g.player.State().Queued[0].SpotifyID != "original" {
		t.Fatal("recovery did not preserve settings/metadata")
	}
	g.announcementChannel.Store(100)
	b.saveSettings("1", g)
	settings, err := db.LoadSettings(ctx, "1")
	if err != nil || settings["announcement_channel"] != "100" {
		t.Fatal(settings, err)
	}
	other := b.getGuild(2)
	if other.conn != nil || other.player.Length() != 0 || other.announcementChannel.Load() != 0 {
		t.Fatal("guild state leaked")
	}
}
