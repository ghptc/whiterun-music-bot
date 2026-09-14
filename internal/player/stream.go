package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"whiterun/internal/media"
)

type Streamer struct {
	YTDLP, FFmpeg                     string
	ResolveTimeout, FirstAudioTimeout time.Duration
}

// Play pipes yt-dlp into FFmpeg, then sends paced Opus packets. There are no
// audio files, signed stream URLs in queue state, or shell commands.
func (s Streamer) Play(parent context.Context, t media.Track, v Voice) (err error) {
	started := time.Now()
	t.Timing.Event("playback_start", started)
	defer func() { t.Timing.Event("playback_complete", started, "error", err) }()
	ctx, cancelCause := context.WithCancelCause(media.WithTiming(parent, t.Timing))
	cancel := func() { cancelCause(context.Canceled) }
	defer cancel()
	defer func() {
		t.Timing.Event("playback_context_complete", started, "cause", context.Cause(ctx), "parent_error", parent.Err())
	}()
	resolved, firstAudio := make(chan struct{}), make(chan struct{})
	var resolvedOnce sync.Once
	markResolved := func() { resolvedOnce.Do(func() { close(resolved) }) }
	resolveTimeout, audioTimeout := s.ResolveTimeout, s.FirstAudioTimeout
	if resolveTimeout <= 0 {
		resolveTimeout = 30 * time.Second
	}
	if audioTimeout <= 0 {
		audioTimeout = 30 * time.Second
	}
	go watchStartup(ctx, cancelCause, resolved, firstAudio, resolveTimeout, audioTimeout)
	input, output, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	defer output.Close()
	yt := media.Command(ctx, s.YTDLP, append(media.BaseArgs(), "--print-to-file", "before_dl:"+streamResolvedMarker+" %(id)s", "/dev/stderr", "-f", "bestaudio/best", "-o", "-", "--", t.URL)...)

	// A successful producer can exit while its buffered bytes are still being
	// paced downstream. Do not apply Command's short post-exit I/O drain limit.
	// Track cancellation kills FFmpeg too, unblocking the pipe writer.
	yt.WaitDelay = 0
	ytErr := &media.LimitedBuffer{Limit: 8192}
	streamStart := time.Now()
	streamTiming := &streamTimingWriter{buffer: ytErr, timing: t.Timing, start: streamStart, tail: "\n", onResolved: markResolved}
	yt.Stderr = streamTiming
	yt.Stdout = &firstWrite{writer: output, first: func() {
		t.Timing.Event("yt_dlp_first_byte", streamStart)
		markResolved()
	}, written: func() { t.Timing.Event("ffmpeg_first_input", streamStart) }}

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
	// FFmpeg owns its read descriptor; release the parent copy.
	input.Close()
	// yt's Go stdout copier owns the write descriptor until Wait drains it.
	ytDone := make(chan error, 1)
	go func() {
		waitErr := yt.Wait()
		output.Close()
		t.Timing.Event("yt_dlp_complete", streamStart, "purpose", "playback", "candidates", 1, "error", waitErr)
		ytDone <- waitErr
	}()
	t.Timing.Event("stream_process_started", streamStart, "pid", yt.Process.Pid)
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = v.Speaking(cleanup, false)
	}()
	speaking := false
	firstFrame := true
	next := time.Now()
	readErr := oggPackets(&firstRead{reader: audio, first: func() { t.Timing.Event("ffmpeg_first_output", streamStart) }}, func(packet []byte) error {
		if firstFrame {
			t.Timing.Event("first_opus_frame", streamStart)
			close(firstAudio)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
		if firstFrame {
			t.Timing.Event("first_voice_frame_attempt", streamStart)
		}
		if err := v.WriteFrame(ctx, packet); err != nil {
			return fmt.Errorf("voice frame send: %w", err)
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
	}, func() { t.Timing.Event("first_ogg_packet", streamStart) })
	if readErr != nil {
		cancelCause(readErr)
	}
	ffWait := ff.Wait()
	t.Timing.Event("ffmpeg_complete", ffStart, "error", ffWait)
	// FFmpeg may exit early while the producer is still fetching data.
	if ffWait != nil {
		cancel()
	}
	ytWait := <-ytDone
	if !streamTiming.resolved {
		t.Timing.Event("stream_resolve_incomplete", streamStart, "error", ytWait)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	var failures []error
	if cause := context.Cause(ctx); cause != nil {
		failures = append(failures, cause)
	}
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
	buffer     *media.LimitedBuffer
	timing     *media.Timing
	start      time.Time
	tail       string
	resolved   bool
	onResolved func()
}

func (w *streamTimingWriter) Write(p []byte) (int, error) {
	if !w.resolved {
		combined := w.tail + string(p)
		marker := "\n" + streamResolvedMarker + " "
		if strings.Contains(combined, marker) {
			w.timing.Event("stream_resolve_complete", w.start)
			w.resolved = true
			w.timing.Event("stream_url_resolved", w.start)
			if w.onResolved != nil {
				w.onResolved()
			}
		}
		if len(combined) > len(marker) {
			combined = combined[len(combined)-len(marker):]
		}
		w.tail = combined
	}
	return w.buffer.Write(p)
}

// Startup deadlines supervise readiness; neither deadline owns subprocesses
// after the first Opus frame. Track cancellation alone owns playback lifetime.
func watchStartup(ctx context.Context, cancel context.CancelCauseFunc, resolved, firstAudio <-chan struct{}, resolveTimeout, audioTimeout time.Duration) {
	timer := time.NewTimer(resolveTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-firstAudio:
		return
	case <-resolved:
	case <-timer.C:
		cancel(fmt.Errorf("stream resolution timeout: %w", context.DeadlineExceeded))
		return
	}
	timer.Reset(audioTimeout)
	select {
	case <-ctx.Done():
	case <-firstAudio:
	case <-timer.C:
		cancel(fmt.Errorf("first Opus frame timeout: %w", context.DeadlineExceeded))
	}
}

type firstWrite struct {
	writer         io.Writer
	first, written func()
	seen           bool
}

func (w *firstWrite) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	first := !w.seen
	if first {
		w.seen = true
		w.first()
	}
	n, err := w.writer.Write(p)
	if first && n > 0 {
		w.written()
	}
	return n, err
}

type firstRead struct {
	reader io.Reader
	first  func()
	seen   bool
}

func (r *firstRead) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && !r.seen {
		r.seen = true
		r.first()
	}
	return n, err
}
