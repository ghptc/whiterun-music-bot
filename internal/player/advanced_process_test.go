package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"whiterun/internal/media"
)

type advancedFrameVoice struct {
	frames atomic.Int32
	ready  chan struct{}
}

func (v *advancedFrameVoice) WriteFrame(context.Context, []byte) error {
	if v.frames.Add(1) == 1 {
		close(v.ready)
	}
	return nil
}
func (*advancedFrameVoice) Speaking(context.Context, bool) error { return nil }
func (*advancedFrameVoice) Close(context.Context)                {}

func TestAdvancedFFmpegKilledAfterFirstFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg fault injection")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable")
	}
	data, err := os.ReadFile(wav(t))
	if err != nil {
		t.Fatal(err)
	}
	// Twenty seconds of PCM forces pipe backpressure while frames are paced.
	data = append(data[:44], make([]byte, 48000*4*20)...)
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	dir := t.TempDir()
	audio := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audio, data, 0600); err != nil {
		t.Fatal(err)
	}
	ytPID, ffPID := filepath.Join(dir, "yt.pid"), filepath.Join(dir, "ff.pid")
	yt := executable(t, "yt-dlp", fmt.Sprintf("echo $$ > '%s'\ncat '%s'\nexec sleep 60\n", ytPID, audio))
	decoder := executable(t, "ffmpeg", fmt.Sprintf("echo $$ > '%s'\nexec '%s' \"$@\"\n", ffPID, ff))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	voice := &advancedFrameVoice{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- (Streamer{YTDLP: yt, FFmpeg: decoder, DrainTimeout: time.Second}).Play(ctx, media.Track{URL: "https://youtu.be/abcdefghijk"}, voice)
	}()
	select {
	case <-voice.ready:
	case err := <-done:
		t.Fatal("stream failed before injected fault", err)
	case <-ctx.Done():
		t.Fatal("first frame missing")
	}
	readPID := func(path string) int {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || id <= 0 {
			t.Fatal("invalid fixture PID", id, err)
		}
		return id
	}
	decoderPID := readPID(ffPID)
	if err := syscall.Kill(decoderPID, syscall.SIGKILL); err != nil {
		t.Fatal("decoder exited before fault injection", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unexpected FFmpeg death reported success")
		}
	case <-ctx.Done():
		t.Fatal("dead decoder stranded pipeline")
	}
	for _, pid := range []int{decoderPID, readPID(ytPID)} {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("owned process %d not reaped: %v", pid, err)
		}
	}
}
