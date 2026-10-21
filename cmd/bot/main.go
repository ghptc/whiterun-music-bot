package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/discord"
	"whiterun/internal/media"
	"whiterun/internal/storage"
)

func main() { os.Exit(mainExit()) }
func mainExit() int {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	console := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 && os.Args[1] == "logs" {
		if err := logsCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("logs_query_failed", "error", err)
			return 1
		}
		return 0
	}
	log, store := persistentLogger(console)
	// Third-party packages using slog.Default share the same structured sink.
	slog.SetDefault(log)
	result := 0
	if err := run(log); err != nil {
		log.Error("bot stopped", "error", err)
		result = 1
	}
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Close(ctx); err != nil {
			console.Warn("log_persistence_flush_timeout")
		}
	}
	return result
}
func configuredSpotify(log *slog.Logger) *media.Spotify {
	return &media.Spotify{Log: log, ClientID: strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_ID")), ClientSecret: strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_SECRET")), RefreshToken: strings.TrimSpace(os.Getenv("SPOTIFY_REFRESH_TOKEN")), Market: strings.TrimSpace(os.Getenv("SPOTIFY_MARKET"))}
}

func run(log *slog.Logger) error {
	var shutdownTimer *time.Timer
	defer func() {
		if shutdownTimer != nil {
			shutdownTimer.Stop()
		}
	}()
	if len(os.Args) > 1 && os.Args[1] == "spotify-diagnose" {
		return diagnoseSpotify(log, os.Args[2:])
	}
	token := strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
	if token == "" {
		return fmt.Errorf("DISCORD_TOKEN is required")
	}
	app, err := snowflake.Parse(os.Getenv("DISCORD_APPLICATION_ID"))
	if err != nil || app == 0 {
		return fmt.Errorf("DISCORD_APPLICATION_ID must be a valid Discord application ID")
	}
	var guild snowflake.ID
	if value := os.Getenv("DISCORD_GUILD_ID"); value != "" {
		guild, err = snowflake.Parse(value)
		if err != nil || guild == 0 {
			return fmt.Errorf("DISCORD_GUILD_ID must be a valid guild ID")
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "commands-sync" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		b, err := discord.New(ctx, token, app, log)
		if err != nil {
			return err
		}
		defer b.Close(context.Background())
		return b.SyncCommands(ctx, guild)
	}
	for _, binary := range []string{"yt-dlp", "ffmpeg", "deno"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("required runtime %s: %w", binary, err)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	b, err := discord.New(ctx, token, app, log)
	if err != nil {
		return err
	}
	b.ConfigureSpotify(configuredSpotify(log))
	policy := media.DefaultMatchPolicy()
	if raw := os.Getenv("SPOTIFY_MATCH_POLICY"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return fmt.Errorf("SPOTIFY_MATCH_POLICY: %w", err)
		}
	}
	if err := b.ConfigureMatchPolicy(policy); err != nil {
		return err
	}
	path := operationalPath()
	db, err := storage.Open(path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	b.ConfigureStore(db)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b.Close(ctx)
	}()
	if err := b.Start(guild); err != nil {
		return fmt.Errorf("Discord startup: %w", err)
	}
	go monitorRuntime(ctx, slog.New(slog.NewJSONHandler(os.Stdout, nil)), func(probe context.Context) runtimeHealth {
		h := runtimeHealth{CheckedAt: time.Now(), ProcessAlive: true, Runtime: b.Health()}
		h.SQLiteAccessible = db.CheckHealth(probe) == nil
		h.SessionRecoveryAvailable = h.SQLiteAccessible
		for _, ph := range h.Runtime.Players {
			if ph.RecoveryExhausted {
				h.SessionRecoveryAvailable = false
			}
		}
		return h
	}, func() { os.Exit(1) }, path+".supervision.json")
	<-ctx.Done()
	// A dependency ignoring cancellation must not hold SIGTERM forever. The
	// SQLite WAL and last committed queue remain recoverable after forced exit.
	shutdownTimer = time.AfterFunc(25*time.Second, func() {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("process_supervision_triggered", "component", "shutdown", "cause", "grace_deadline_exceeded")
		os.Exit(1)
	})
	log.Info("shutting down")
	return nil
}
