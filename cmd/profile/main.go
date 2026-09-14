// profile measures real resolver latency without connecting to Discord.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/player"
)

type firstFrameVoice struct {
	cancel    context.CancelFunc
	sent      bool
	completed bool
	frames    int
	playFor   time.Duration
	first     time.Time
}

func (v *firstFrameVoice) WriteFrame(context.Context, []byte) error {
	v.sent = true
	v.frames++
	if v.first.IsZero() {
		v.first = time.Now()
	}
	if time.Since(v.first) >= v.playFor {
		v.completed = true
		v.cancel()
	}
	return nil
}
func (firstFrameVoice) Speaking(context.Context, bool) error { return nil }
func (firstFrameVoice) Close(context.Context)                {}

func main() {
	playback := flag.Bool("playback", false, "also measure playback through the first local Opus frame (no Discord)")
	playFor := flag.Duration("play-for", 0, "keep consuming paced local audio for this duration after first frame; requires -playback")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if flag.NArg() == 0 {
		log.Error("provide one or more song queries or YouTube URLs")
		os.Exit(2)
	}
	resolver := media.Resolver{Binary: "yt-dlp", Cache: &media.SearchCache{}}
	failed := false
	for _, query := range flag.Args() {
		started := time.Now()
		timing := media.NewTiming(log.With("query", query))
		ctx := media.WithTiming(context.Background(), timing)
		track, err := resolver.Resolve(ctx, query)
		timing.Event("profile_resolution_total", started, "error", err)
		if err != nil {
			failed = true
			continue
		}
		if *playback {
			playCtx, cancel := context.WithTimeout(ctx, 90*time.Second+*playFor)
			sink := &firstFrameVoice{cancel: cancel, playFor: *playFor}
			result := make(chan error, 1)
			guild := player.New(playCtx, log, func(ctx context.Context, track media.Track, voice player.Voice) error {
				err := (player.Streamer{YTDLP: "yt-dlp", FFmpeg: "ffmpeg"}).Play(ctx, track, voice)
				result <- err
				return err
			})
			err = guild.Attach(sink)
			queueStart := time.Now()
			if err == nil {
				err = guild.Enqueue(track, guild.Ticket())
			}
			timing.Event("queue_inserted", queueStart, "measurement", "local_queue", "error", err)
			timing.Event("profile_queue_ready", started, "discord_queued_ms", nil, "error", err)
			if err == nil {
				err = <-result
			}
			cancel()
			guild.Close(context.Background())
			if sink.completed {
				err = nil
			} else {
				failed = true
			}
			timing.Event("profile_playback_complete", started, "audio_sink", "local_discard", "frame_received", sink.sent, "frames", sink.frames, "requested_play_duration", playFor.String(), "error", err)
		}
	}
	if failed {
		os.Exit(1)
	}
}
