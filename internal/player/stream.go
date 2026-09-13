package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"whiterun/internal/media"
)

type Streamer struct{ YTDLP, FFmpeg string }

// Play pipes yt-dlp into FFmpeg, then sends paced Opus packets. There are no
// audio files, signed stream URLs in queue state, or shell commands.
func (s Streamer) Play(parent context.Context, t media.Track, v Voice) (err error) {
	started := time.Now()
	t.Timing.Event("playback_start", started)
	defer func() { t.Timing.Event("playback_complete", started, "error", err) }()
	ctx, cancel := context.WithTimeout(parent, t.Duration+2*time.Minute)
	defer cancel()
	input, output, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer output.Close()
	yt := media.Command(ctx, s.YTDLP, append(media.BaseArgs(), "--print-to-file", "before_dl:"+streamResolvedMarker+" %(id)s", "/dev/stderr", "-f", "bestaudio/best", "-o", "-", "--", t.URL)...)
	yt.Stdout = output
	ytErr := &media.LimitedBuffer{Limit: 8192}
	streamStart := time.Now()
	streamTiming := &streamTimingWriter{buffer: ytErr, timing: t.Timing, start: streamStart, tail: "\n"}
	yt.Stderr = streamTiming
	ff := media.Command(ctx, s.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", "pipe:0", "-vn", "-ac", "2", "-ar", "48000", "-c:a", "libopus", "-b:a", "96k", "-vbr", "off", "-application", "audio", "-frame_duration", "20", "-f", "opus", "-flush_packets", "1", "pipe:1")
	ff.Stdin = input
	ffErr := &media.LimitedBuffer{Limit: 8192}
	ff.Stderr = ffErr
	audio, err := ff.StdoutPipe()
	if err != nil {
		return err
	}
	ffStart := time.Now()
	if err = ff.Start(); err != nil {
		audio.Close()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	t.Timing.Event("ffmpeg_start", ffStart, "purpose", "audio_decode", "category", "ffmpeg", "started_at", ffStart.UTC(), "candidates", 1)
	t.Timing.Event("stream_resolve_start", streamStart)
	t.Timing.Event("yt_dlp_start", streamStart, "purpose", "playback", "category", "stream", "started_at", streamStart.UTC(), "candidates", 1, "video_id", t.VideoID)
	if err = yt.Start(); err != nil {
		cancel()
		output.Close()
		input.Close()
		_ = ff.Wait()
		return fmt.Errorf("start yt-dlp: %w", err)
	}
	// Children hold their own descriptors. Closing parent copies permits EOF.
	input.Close()
	output.Close()
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = v.Speaking(cleanup, false)
	}()
	// A stalled producer must not block this guild for an entire song duration.
	idle := time.AfterFunc(60*time.Second, cancel)
	defer idle.Stop()
	speaking := false
	firstFrame := true
	next := time.Now()
	readErr := oggPackets(audio, func(packet []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		idle.Reset(30 * time.Second)
		if !speaking {
			if err := v.Speaking(ctx, true); err != nil {
				return err
			}
			speaking = true
			next = time.Now()
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := v.WriteFrame(ctx, packet); err != nil {
			return err
		}
		if firstFrame {
			t.Timing.Event("first_audio_frame", started)
			firstFrame = false
		}
		next = next.Add(20 * time.Millisecond)
		if time.Since(next) > 60*time.Millisecond {
			next = time.Now().Add(20 * time.Millisecond)
		}
		return nil
	})
	if readErr != nil {
		cancel()
	}
	ffWait := ff.Wait()
	// FFmpeg may exit early while the producer is still fetching data.
	if ffWait != nil {
		cancel()
	}
	ytWait := yt.Wait()
	t.Timing.Event("yt_dlp_complete", streamStart, "purpose", "playback", "candidates", 1, "error", ytWait)
	if !streamTiming.resolved {
		t.Timing.Event("stream_resolve_incomplete", streamStart, "error", ytWait)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	var failures []error
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		failures = append(failures, fmt.Errorf("audio stream: %w", readErr))
	}
	if ffWait != nil {
		failures = append(failures, fmt.Errorf("FFmpeg failed: %w: %s", ffWait, strings.TrimSpace(string(ffErr.Data))))
	}
	if ytWait != nil {
		failures = append(failures, fmt.Errorf("yt-dlp stream failed: %w: %s", ytWait, strings.TrimSpace(string(ytErr.Data))))
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	// Discord recommends five silence frames at the end of a transmission.
	for range 5 {
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := v.WriteFrame(ctx, []byte{0xf8, 0xff, 0xfe}); err != nil {
			return err
		}
	}
	return nil
}

// yt-dlp emits this marker after extraction, immediately before download. Keep
// it off stdout (the audio pipe) and never log signed media URLs.
const streamResolvedMarker = "BARD_STREAM_RESOLVED"

type streamTimingWriter struct {
	buffer   *media.LimitedBuffer
	timing   *media.Timing
	start    time.Time
	tail     string
	resolved bool
}

func (w *streamTimingWriter) Write(p []byte) (int, error) {
	if !w.resolved {
		combined := w.tail + string(p)
		marker := "\n" + streamResolvedMarker + " "
		if strings.Contains(combined, marker) {
			w.timing.Event("stream_resolve_complete", w.start)
			w.resolved = true
		}
		if len(combined) > len(marker) {
			combined = combined[len(combined)-len(marker):]
		}
		w.tail = combined
	}
	return w.buffer.Write(p)
}
