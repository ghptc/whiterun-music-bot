package player

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
	"whiterun/internal/media"
)

type startupVoice struct {
	fakeVoice
	first  time.Time
	cancel context.CancelFunc
}

func (v *startupVoice) WriteFrame(context.Context, []byte) error {
	if v.first.IsZero() {
		v.first = time.Now()
		v.cancel()
	}
	return nil
}

// A real-time source must reach Discord before its Ogg page fills or EOF.
func TestStreamerRealtimeFirstFrame(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	yt := executable(t, "source", "exec '"+ff+"' -hide_banner -loglevel error -re -f lavfi -i sine=frequency=440:sample_rate=48000 -t 4 -c:a libopus -f opus -page_duration 20000 pipe:1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	v := &startupVoice{cancel: cancel}
	started := time.Now()
	err = (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{}, v)
	if !errors.Is(err, context.Canceled) || v.first.IsZero() {
		t.Fatal(err, v.first)
	}
	if delay := v.first.Sub(started); delay > time.Second {
		t.Fatalf("first frame delayed by muxer buffering: %v", delay)
	}
}
