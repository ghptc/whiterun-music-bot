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
func (s Streamer) Play(parent context.Context, t media.Track, v Voice) error {
	ctx, cancel := context.WithTimeout(parent, t.Duration+2*time.Minute)
	defer cancel()
	input, output, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer output.Close()
	yt := media.Command(ctx, s.YTDLP, append(media.BaseArgs(), "-f", "bestaudio/best", "-o", "-", "--", t.URL)...)
	yt.Stdout = output
	ytErr := &media.LimitedBuffer{Limit: 8192}
	yt.Stderr = ytErr
	ff := media.Command(ctx, s.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", "pipe:0", "-vn", "-ac", "2", "-ar", "48000", "-c:a", "libopus", "-b:a", "96k", "-vbr", "off", "-application", "audio", "-frame_duration", "20", "-f", "opus", "-flush_packets", "1", "pipe:1")
	ff.Stdin = input
	ffErr := &media.LimitedBuffer{Limit: 8192}
	ff.Stderr = ffErr
	audio, err := ff.StdoutPipe()
	if err != nil {
		return err
	}
	if err = ff.Start(); err != nil {
		audio.Close()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
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
