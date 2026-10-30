package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"whiterun/internal/media"
)

type Streamer struct {
	YTDLP, FFmpeg                     string
	ResolveTimeout, FirstAudioTimeout time.Duration
	IdleTimeout, DrainTimeout         time.Duration
}

// Play pipes yt-dlp into FFmpeg, then sends paced Opus packets. There are no
// audio files, signed stream URLs in queue state, or shell commands.
func (s Streamer) Play(parent context.Context, t media.Track, v Voice) (err error) {
	started := time.Now()
	if t.ResumeOffset < 0 || (t.Live && t.ResumeOffset > 0) {
		return &StreamError{"resume_unsupported", errors.New("saved progress cannot be sought in this source; position retained for explicit skip")}
	}
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
		resolveTimeout = 20 * time.Second
	}
	if audioTimeout <= 0 {
		audioTimeout = 20 * time.Second
	}
	monitorCtx, stopMonitors := context.WithCancel(ctx)
	var monitors sync.WaitGroup
	monitors.Add(2)
	var activity atomic.Int64
	activity.Store(time.Now().UnixNano())
	idle := s.IdleTimeout
	if idle <= 0 {
		idle = 45 * time.Second
	}
	go func() {
		defer monitors.Done()
		watchStartup(monitorCtx, cancelCause, resolved, firstAudio, resolveTimeout, audioTimeout)
	}()
	go func() { defer monitors.Done(); watchActivity(monitorCtx, cancelCause, &activity, idle) }()
	defer func() { stopMonitors(); monitors.Wait() }()
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
	ytErr := &media.LimitedBuffer{Limit: 512, Tail: true}
	streamStart := time.Now()
	streamTiming := &streamTimingWriter{buffer: ytErr, timing: t.Timing, start: streamStart, tail: "\n", onResolved: markResolved}
	yt.Stderr = streamTiming
	sourceReady := make(chan struct{})
	yt.Stdout = &firstWrite{writer: output, first: func() {
		close(sourceReady)
		t.Timing.Event("yt_dlp_first_byte", streamStart)
		markResolved()
	}, written: func() { t.Timing.Event("ffmpeg_first_input", streamStart) }}

	ffArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", "pipe:0", "-vn", "-ac", "2", "-ar", "48000", "-c:a", "libopus", "-b:a", "96k", "-vbr", "off", "-application", "audio", "-frame_duration", "20", "-f", "opus", "-flush_packets", "1", "-page_duration", "20000", "pipe:1"}
	// Accurate output seek decodes/discards preceding audio. No signed URL or
	// unreliable byte-range seek; timeout preserves the position rather than
	// silently replaying the beginning.
	if t.ResumeOffset > 0 {
		ffArgs = append(ffArgs[:7], append([]string{"-ss", fmt.Sprintf("%.3f", t.ResumeOffset.Seconds())}, ffArgs[7:]...)...)
		t.Timing.Event("playback_resume_started", started, "confirmed_ms", t.ResumeOffset.Milliseconds(), "seek", "decode_and_discard")
	}
	ff := media.Command(ctx, s.FFmpeg, ffArgs...)
	ff.Stdin = input
	ffErr := &media.LimitedBuffer{Limit: 512, Tail: true}
	ff.Stderr = ffErr
	audio, err := ff.StdoutPipe()
	if err != nil {
		return err
	}
	defer audio.Close()
	t.Timing.Event("stream_resolve_start", streamStart)
	t.Timing.Event("yt_dlp_start", streamStart, "purpose", "playback", "category", "stream", "started_at", streamStart.UTC(), "candidates", 1, "video_id", t.VideoID)
	if err = yt.Start(); err != nil {
		return &StreamError{"yt_dlp_start", fmt.Errorf("start yt-dlp: %w", err)}
	}
	ytDone := make(chan error, 1)
	var ytFinished, ffFinished atomic.Bool
	go func() {
		waitErr := yt.Wait()
		ytFinished.Store(true)
		t.Timing.Event("yt_dlp_complete", streamStart, "purpose", "playback", "candidates", 1, "error", waitErr, "cancel_reason", context.Cause(ctx))
		ytDone <- waitErr
		output.Close()
	}()
	// Do not launch a decoder for empty output from a rejected extraction.
	// Supervise the producer's pipes even if its leader exits before helpers.
	preForced := make(chan struct{})
	stopPre := context.AfterFunc(ctx, func() {
		defer close(preForced)
		if !ytFinished.Load() {
			_ = syscall.Kill(-yt.Process.Pid, syscall.SIGKILL)
		}
		output.Close()
	})
	defer func() {
		if !stopPre() {
			<-preForced
		}
	}()
	var earlyErr error
	producerEnded := false
	select {
	case <-sourceReady:
	case earlyErr = <-ytDone:
		producerEnded = true
	case <-ctx.Done():
		earlyErr = <-ytDone
		producerEnded = true
	}
	if producerEnded {
		select {
		case <-sourceReady:
			// A small valid source may have finished before decoder startup.
			ytDone <- earlyErr
		default:
			defer func() {
				t.Timing.Event("audio_source_exit", streamStart, "video_id", t.VideoID, "spotify_id", t.SpotifyID, "yt_exit_code", exitCode(yt), "decoder_started", false, "category", failureCategory(err))
			}()
			if parent.Err() != nil {
				return parent.Err()
			}
			if earlyErr != nil && exitCode(yt) > 0 {
				return &StreamError{sourceFailure(ytErr.Data), fmt.Errorf("exit_code=%d: %w: %s", exitCode(yt), earlyErr, sourceFailure(ytErr.Data))}
			}
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return &StreamError{"no_audio", errors.New("source ended before producing audio")}
		}
	}
	ffStart := time.Now()
	if err = ff.Start(); err != nil {
		cancel()
		output.Close()
		<-ytDone
		return &StreamError{"ffmpeg_start", fmt.Errorf("start FFmpeg: %w", err)}
	}
	t.Timing.Event("ffmpeg_start", ffStart, "purpose", "audio_decode", "category", "ffmpeg", "started_at", ffStart.UTC(), "candidates", 1)
	input.Close()
	// os/exec can retire its context watcher after reaping a leader even while
	// Wait still drains helpers' inherited pipes. The pipeline owns cancellation
	// until ALL process I/O completes, independent of that watcher.
	forced := make(chan struct{})
	stopForce := context.AfterFunc(ctx, func() {
		defer close(forced)
		if !ytFinished.Load() {
			_ = syscall.Kill(-yt.Process.Pid, syscall.SIGKILL)
		}
		if !ffFinished.Load() {
			_ = syscall.Kill(-ff.Process.Pid, syscall.SIGKILL)
		}
		output.Close()
		audio.Close()
	})
	defer func() {
		if !stopForce() {
			<-forced
		}
	}()
	t.Timing.Event("audio_source_start", streamStart, "video_id", t.VideoID, "live", t.Live, "yt_pid", yt.Process.Pid, "ffmpeg_pid", ff.Process.Pid)
	t.Timing.Event("stream_process_started", streamStart, "pid", yt.Process.Pid)
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = v.Speaking(cleanup, false)
	}()
	ffReaped, ytReaped := false, false
	defer func() {
		if value := recover(); value != nil {
			cancel()
			audio.Close()
			output.Close()
			if !ffReaped {
				_ = ff.Wait()
				ffFinished.Store(true)
			}
			if !ytReaped {
				<-ytDone
			}
			err = &StreamError{"playback_panic", fmt.Errorf("stream callback panic (%T)", value)}
		}
	}()
	speaking := false
	firstFrame := true
	sentFrames := int64(0)
	next := time.Now()
	readErr := oggPackets(&firstRead{reader: audio, first: func() { t.Timing.Event("ffmpeg_first_output", streamStart) }}, func(packet []byte) error {
		if firstFrame {
			t.Timing.Event("first_opus_packet", streamStart)
			t.Timing.Event("first_opus_frame", streamStart)
			close(firstAudio)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !speaking {
			if err := v.Speaking(ctx, true); err != nil {
				return &StreamError{"voice_speaking", err}
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
		// Wait after pacing so a pause during that timer cannot send a packet.
		// The parser stops reading here: bounded pipe backpressure holds the
		// same yt-dlp/FFmpeg processes and audio position without buffering RAM.
		waited, err := waitPlayback(ctx)
		if err != nil {
			return err
		}
		if waited {
			next = time.Now()
		}
		if firstFrame {
			t.Timing.Event("first_voice_frame_attempt", streamStart)
		}
		if err := v.WriteFrame(ctx, packet); err != nil {
			return &StreamError{"voice_send", err}
		}
		activity.Store(time.Now().UnixNano())
		sentFrames++
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
	if readErr == nil && firstFrame {
		readErr = &StreamError{"no_audio", errors.New("source ended without Opus audio packets")}
	}
	if readErr != nil {
		cancelCause(readErr)
	}
	// EOF ends the parser, not necessarily the processes. Bound both waits.
	drain := s.DrainTimeout
	if drain <= 0 {
		drain = 2 * time.Second
	}
	timer := time.NewTimer(drain)
	defer timer.Stop()
	ffDone := make(chan error, 1)
	go func() { result := ff.Wait(); ffFinished.Store(true); ffDone <- result }()
	var ffWait, ytWait error
	select {
	case ffWait = <-ffDone:
	case <-timer.C:
		cancelCause(&StreamError{"decoder_after_eof", errors.New("decoder remained alive after audio EOF")})
		ffWait = <-ffDone
	case <-ctx.Done():
		ffWait = <-ffDone
	}
	ffReaped = true
	t.Timing.Event("ffmpeg_complete", ffStart, "exit_code", exitCode(ff), "error", ffWait)
	if ffWait != nil {
		cancel()
	}
	select {
	case ytWait = <-ytDone:
	case <-timer.C:
		cancelCause(&StreamError{"source_after_eof", errors.New("source remained alive after audio EOF")})
		ytWait = <-ytDone
	case <-ctx.Done():
		ytWait = <-ytDone
	}

	ytReaped = true
	if !streamTiming.resolved {
		t.Timing.Event("stream_resolve_incomplete", streamStart, "error", ytWait)
	}
	if parent.Err() != nil {
		t.Timing.Event("audio_source_exit", streamStart, "video_id", t.VideoID, "yt_exit_code", exitCode(yt), "ffmpeg_exit_code", exitCode(ff), "category", "canceled", "cancel_reason", context.Cause(parent))
		return parent.Err()
	}
	var failures []error
	if cause := context.Cause(ctx); cause != nil {
		failures = append(failures, cause)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		failures = append(failures, fmt.Errorf("audio stream: %w", readErr))
	}
	// A positive producer exit is the root cause when decoding its empty output
	// also fails. Killed processes are cleanup consequences, not source diagnoses.
	if ytWait != nil && exitCode(yt) > 0 {
		failures = append([]error{&StreamError{sourceFailure(ytErr.Data), fmt.Errorf("exit_code=%d: %w: %s", exitCode(yt), ytWait, sourceFailure(ytErr.Data))}}, failures...)
	}
	if ffWait != nil && exitCode(ff) > 0 {
		failures = append(failures, &StreamError{"ffmpeg_exit", fmt.Errorf("exit_code=%d: %w: %s", exitCode(ff), ffWait, media.SafeDiagnostic(ffErr.Data))})
	}
	if len(failures) == 0 && ytWait != nil {
		failures = append(failures, &StreamError{"yt_dlp_exit", ytWait})
	}
	if len(failures) == 0 && ffWait != nil {
		failures = append(failures, &StreamError{"ffmpeg_exit", ffWait})
	}
	ytTail, ffTail := "", ""
	if len(failures) > 0 {
		ytTail, ffTail = sourceFailure(ytErr.Data), media.SafeDiagnostic(ffErr.Data)
	}
	t.Timing.Event("audio_source_exit", streamStart, "video_id", t.VideoID, "yt_exit_code", exitCode(yt), "ffmpeg_exit_code", exitCode(ff), "category", failureCategory(errors.Join(failures...)), "yt_stderr_tail", ytTail, "ffmpeg_stderr_tail", ffTail)

	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if !t.Live && t.Duration > 0 && t.ResumeOffset+time.Duration(sentFrames)*20*time.Millisecond+2*time.Second < t.Duration {
		return &StreamError{"audio_eof", errors.New("audio ended before expected recording duration")}
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

func sourceFailure(stderr []byte) string {
	if err := media.ClassifyExtraction(stderr, nil); err != nil {
		switch category := media.ExtractionCategory(err); category {
		case "youtube_bot_challenge", "youtube_auth_required", "youtube_rate_limited", "youtube_network_timeout", "youtube_video_unavailable":
			return category
		}
	}
	text := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(text, "sign in to confirm") || strings.Contains(text, "confirm you're not a bot") || strings.Contains(text, "confirm you’re not a bot") || strings.Contains(text, "authentication required") || strings.Contains(text, "login required"):
		return "youtube_auth_challenge"
	case strings.Contains(text, "too many requests") || strings.Contains(text, "rate limit") || strings.Contains(text, "http error 429") || strings.Contains(text, " 429 "):
		return "youtube_rate_limited"
	case strings.Contains(text, "temporary failure in name resolution"), strings.Contains(text, "name or service not known"), strings.Contains(text, "connection timed out"), strings.Contains(text, "network is unreachable"), strings.Contains(text, "connection reset"), strings.Contains(text, "http error 500"), strings.Contains(text, "http error 502"), strings.Contains(text, "http error 503"):
		return "source_network"
	case strings.Contains(text, "403"):
		return "source_http_403"
	case strings.Contains(text, "410"):
		return "source_gone"
	case strings.Contains(text, "expired"):
		return "source_expired"
	case strings.Contains(text, "broken pipe"):
		return "source_broken_pipe"
	default:
		return "yt_dlp_exit"
	}
}
