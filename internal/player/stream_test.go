package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whiterun/internal/media"
)

// These tests exercise real local FFmpeg with synthetic PCM, never YouTube.
func executable(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func wav(t *testing.T) string {
	t.Helper()
	samples := 4800
	b := make([]byte, 44+samples*4)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 48000*4)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(samples*4))
	p := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

type recordingVoice struct {
	fakeVoice
	frames   int
	speaking bool
}

func (v *recordingVoice) WriteFrame(context.Context, []byte) error  { v.frames++; return nil }
func (v *recordingVoice) Speaking(_ context.Context, on bool) error { v.speaking = on; return nil }
func TestStreamerLocalFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg unavailable; Docker runtime also checks libopus")
	}
	file := wav(t)
	yt := executable(t, "yt-dlp", "cat '"+file+"'\n")
	v := &recordingVoice{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{Duration: time.Second}, v); err != nil {
		t.Fatal(err)
	}
	if v.frames < 10 || v.speaking {
		t.Fatalf("frames=%d, speaking=%v", v.frames, v.speaking)
	}
}
func TestStreamerFailuresAndCancellation(t *testing.T) {
	for _, tt := range []struct {
		name, yt, ff string
		cancel       bool
	}{
		{"yt failure", "echo failed >&2; exit 7", "cat >/dev/null; exit 0", false},
		{"ffmpeg failure", "cat /dev/zero", "echo failed >&2; exit 8", false},
		{"cancel stalled processes", "sleep 60", "cat", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			yt := executable(t, "yt-dlp", tt.yt)
			ff := executable(t, "ffmpeg", tt.ff)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if tt.cancel {
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			start := time.Now()
			err := (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{Duration: time.Second}, &fakeVoice{})
			if err == nil {
				t.Fatal("expected failure")
			}
			if time.Since(start) > time.Second {
				t.Fatalf("cleanup too slow: %v", err)
			}
		})
	}
}
func TestStreamerMissingExecutable(t *testing.T) {
	for _, s := range []Streamer{{YTDLP: "/nonexistent/yt-dlp", FFmpeg: executable(t, "ffmpeg", "cat")}, {YTDLP: "/nonexistent/yt-dlp", FFmpeg: "/nonexistent/ffmpeg"}} {
		err := s.Play(context.Background(), media.Track{}, &fakeVoice{})
		if err == nil || !strings.Contains(err.Error(), "start") {
			t.Fatalf("expected start failure, got %v", err)
		}
	}
}

func TestStreamResolutionMarkerAcrossWrites(t *testing.T) {
	var logs bytes.Buffer
	timing := media.NewTiming(slog.New(slog.NewJSONHandler(&logs, nil)))
	w := &streamTimingWriter{buffer: &media.LimitedBuffer{Limit: 8}, timing: timing, start: time.Now()}
	for i, chunk := range []string{strings.Repeat("warning", 100), "\n[info] Writing BARD_STREAM_RESOLVED %(id)s to /dev/stderr\n", "BARD_STREAM_", "RESOLVED abcdefghijk\n", streamResolvedMarker + " abcdefghijk\n"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
		if i < 3 && logs.Len() != 0 {
			t.Fatal("diagnostic or partial marker triggered timing")
		}
	}
	if strings.Count(logs.String(), "stream_resolve_complete") != 1 {
		t.Fatalf("logs: %s", logs.String())
	}
	if !w.buffer.Truncated {
		t.Fatal("diagnostics were not bounded")
	}
}
