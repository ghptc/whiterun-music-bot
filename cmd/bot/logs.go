package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"whiterun/internal/logstore"
)

func operationalPath() string {
	if path := os.Getenv("BARD_DB_PATH"); path != "" {
		return path
	}
	return "bard.db"
}
func logPath() string {
	if path := os.Getenv("BARD_LOG_DB_PATH"); path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(operationalPath()), "bard-logs.db")
}
func persistentLogger(console *slog.Logger) (*slog.Logger, *logstore.Store) {
	maxBytes := logstore.DefaultMaxBytes
	if raw := os.Getenv("BARD_LOG_DB_MAX_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < logstore.MinMaxBytes {
			console.Error("log_persistence_unavailable", "reason", "invalid BARD_LOG_DB_MAX_BYTES")
			return console, nil
		}
		maxBytes = n
	}
	secrets := []string{}
	for _, env := range os.Environ() {
		key, value, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(key)
		for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "COOKIE", "CREDENTIAL", "API_KEY", "CLIENT_ID", "AUTHORIZATION", "PRIVATE_KEY"} {
			if strings.Contains(key, marker) {
				secrets = append(secrets, value)
				break
			}
		}
	}
	store, err := logstore.Open(logstore.Options{Path: logPath(), OperationalPath: operationalPath(), MaxBytes: maxBytes, Diagnostic: console, Secrets: secrets})
	if err != nil {
		console.Error("log_persistence_unavailable", "reason", "SQLite initialization failed", "error_category", logstore.FailureCategory(err))
		return console, nil
	}
	logger := slog.New(logstore.NewHandler(console.Handler(), store))
	logger.Info("log_persistence_started", "max_bytes", maxBytes)
	return logger, store
}
func logsCommand(args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("bard logs", flag.ContinueOnError)
	flags.SetOutput(errOut)
	f := logstore.Filter{}
	path := flags.String("db", logPath(), "log SQLite path (read-only)")
	flags.IntVar(&f.Last, "last", 100, "newest matching records, 1–10000; output in time order")
	flags.StringVar(&f.Level, "level", "", "DEBUG, INFO, WARN or ERROR")
	flags.StringVar(&f.Guild, "guild", "", "guild ID")
	flags.StringVar(&f.Interaction, "interaction", "", "interaction ID")
	flags.StringVar(&f.Track, "track", "", "track, Spotify track or YouTube video ID")
	flags.StringVar(&f.Event, "event", "", "exact event/stage name")
	since := flags.String("since", "", "duration such as 1h, or RFC3339 timestamp")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if f.Last < 1 || f.Last > 10000 {
		return fmt.Errorf("last must be within 1–10000")
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected logs arguments")
	}
	if *since != "" {
		if duration, err := time.ParseDuration(*since); err == nil {
			if duration < 0 {
				return fmt.Errorf("since duration must not be negative")
			}
			f.Since = time.Now().Add(-duration)
		} else {
			timestamp, err := time.Parse(time.RFC3339Nano, *since)
			if err != nil {
				return fmt.Errorf("since must be a duration or RFC3339 timestamp")
			}
			f.Since = timestamp
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return logstore.Read(ctx, *path, f, out)
}
