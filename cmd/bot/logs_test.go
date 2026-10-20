package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogsCLIWithoutDiscordCredentials(t *testing.T) {
	t.Setenv("DISCORD_TOKEN", "")
	t.Setenv("BARD_LOG_DB_PATH", filepath.Join(t.TempDir(), "logs.db"))
	var console bytes.Buffer
	log, store := persistentLogger(slog.New(slog.NewJSONHandler(&console, nil)))
	if store == nil {
		t.Fatal(console.String())
	}
	log.Error("spotify_resolution", "guild_id", "42", "spotify_id", "spotify-track", "video_id", "video-track")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--last", "1"}, {"--level", "ERROR"}, {"--guild", "42"}, {"--track", "spotify-track"}, {"--event", "spotify_resolution"}, {"--since", "1h", "--level", "ERROR"}, {"--since", time.Now().Add(-time.Hour).Format(time.RFC3339), "--guild", "42"}} {
		var out, errOut bytes.Buffer
		if err := logsCommand(args, &out, &errOut); err != nil {
			t.Fatal(args, err, errOut.String())
		}
		if !strings.Contains(out.String(), "spotify_resolution") {
			t.Fatal(args, out.String())
		}
	}
	for _, args := range [][]string{{"--last", "0"}, {"--last", "-1"}, {"--since", "-1h"}, {"--since", "yesterday"}, {"--level", "FATAL"}, {"unexpected"}} {
		var out, errOut bytes.Buffer
		if err := logsCommand(args, &out, &errOut); err == nil {
			t.Fatal(args)
		}
	}
}
func TestPersistentLoggerFallsBackAndKeepsConsole(t *testing.T) {
	for _, quota := range []string{"invalid", "1"} {
		t.Run(quota, func(t *testing.T) {
			t.Setenv("BARD_LOG_DB_MAX_BYTES", quota)
			var out bytes.Buffer
			console := slog.New(slog.NewJSONHandler(&out, nil))
			log, store := persistentLogger(console)
			if store != nil {
				t.Fatal("invalid config opened database")
			}
			log.Info("still_playing")
			if !strings.Contains(out.String(), "log_persistence_unavailable") || !strings.Contains(out.String(), "still_playing") {
				t.Fatal(out.String())
			}
		})
	}
	t.Setenv("BARD_LOG_DB_MAX_BYTES", "1048576")
	t.Setenv("BARD_LOG_DB_PATH", filepath.Join(t.TempDir(), "missing", "logs.db"))
	var out bytes.Buffer
	log, store := persistentLogger(slog.New(slog.NewJSONHandler(&out, nil)))
	if store != nil {
		t.Fatal("unavailable DB opened")
	}
	log.Info("stdout_works")
	if !strings.Contains(out.String(), "stdout_works") {
		t.Fatal(out.String())
	}
}
