// spotify-connect-check plays to the local sound device, without Discord or
// permanent audio files. Output contains only an allowlist of playback evidence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"whiterun/internal/spotifyconnect"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := flag.String("endpoint", "http://127.0.0.1:3678", "loopback API")
	pipe := flag.String("pipe", "", "private s16le stereo 44100Hz FIFO")
	uri := flag.String("uri", "", "exact Spotify track URI")
	timeout := flag.Duration("timeout", 10*time.Minute, "bounded test duration")
	flag.Parse()
	c, err := spotifyconnect.New(*endpoint)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	ctx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(context.Canceled)
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Stopped {
		return errors.New("player occupied; stop it explicitly before diagnostic")
	}
	info, err := os.Lstat(*pipe)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("private FIFO required")
	}
	f, err := os.OpenFile(*pipe, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("cannot open FIFO")
	}
	defer f.Close()
	ready := make(chan struct{})
	identity := make(chan struct{})
	complete := make(chan struct{})
	eventErr := make(chan error, 1)
	var identityOnce, completeOnce sync.Once
	go func() {
		e := c.EventsReady(ctx, ready, func(e spotifyconnect.Event) error {
			switch e.Type {
			case "metadata", "playing", "will_play":
				if e.Data.URI != *uri {
					return spotifyconnect.ErrIdentity
				}
				if e.Type == "metadata" {
					identityOnce.Do(func() { close(identity) })
				}
			case "not_playing":
				if e.Data.URI != *uri {
					return spotifyconnect.ErrIdentity
				}
				completeOnce.Do(func() { close(complete) })
			case "inactive", "playback_error":
				return fmt.Errorf("local playback interrupted: event=%s stage=%s kind=%s unavailable=%t", e.Type, e.Data.Stage, e.Data.Kind, e.Data.Unplayable)
			}
			return nil
		})
		if e != nil {
			cancelCause(e)
		}
		eventErr <- e
	}()
	defer func() { cancel(); f.Close(); <-eventErr }()
	select {
	case <-ready:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Stop(cleanup)
	}()
	started := time.Now()
	if err = c.Play(ctx, *uri); err != nil {
		return err
	}
	select {
	case <-identity:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	cmd := exec.CommandContext(ctx, "ffplay", "-hide_banner", "-loglevel", "error", "-nodisp", "-autoexit", "-f", "s16le", "-ar", "44100", "-ch_layout", "stereo", "-i", "pipe:0")
	// Wrapping forces a cancelable Go copy rather than handing a nonblocking FIFO
	// directly to FFmpeg. The limit is a safety ceiling, never a saved audio file.
	cmd.Stdin = io.LimitReader(f, 44100*4*int64(timeout.Seconds()+1))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return errors.New("local audio player failed to start")
	}
	reaped := false
	defer func() {
		if !reaped {
			cmd.Process.Kill()
			f.Close()
			cmd.Wait()
		}
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	if err = c.Pause(ctx); err != nil {
		return err
	}
	before, err := c.Status(ctx)
	if err != nil {
		return err
	}
	timer.Reset(time.Second)
	select {
	case <-timer.C:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	after, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if before.ValidateIdentity(*uri) != nil || after.ValidateIdentity(*uri) != nil || !after.Paused || after.Track.Position-before.Track.Position > 100 {
		return errors.New("pause position validation failed")
	}
	if err = c.Resume(ctx); err != nil {
		return err
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"authenticated": true, "expected_uri": *uri, "identity_verified": true, "pause_resume_verified": true, "startup_ms": time.Since(started).Milliseconds() - 4000})
	select {
	case <-complete:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	// The writer emits completion after its final samples. Allow the local decoder
	// to drain the small kernel FIFO before closing its Go reader.
	timer.Reset(300 * time.Millisecond)
	select {
	case <-timer.C:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	f.Close()
	if err = cmd.Wait(); err != nil {
		return errors.New("local audio player failed")
	}
	reaped = true
	json.NewEncoder(os.Stdout).Encode(map[string]any{"completed": true, "expected_uri": *uri, "elapsed_ms": time.Since(started).Milliseconds(), "audio_saved": false})
	return nil
}
