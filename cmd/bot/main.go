package main

import (
	"context"
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
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("bot stopped", "error", err)
		os.Exit(1)
	}
}
func run(log *slog.Logger) error {
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
	b.ConfigureSpotify(&media.Spotify{ClientID: strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_ID")), ClientSecret: strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_SECRET")), RefreshToken: strings.TrimSpace(os.Getenv("SPOTIFY_REFRESH_TOKEN")), Market: strings.TrimSpace(os.Getenv("SPOTIFY_MARKET"))})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b.Close(ctx)
	}()
	if err := b.Start(guild); err != nil {
		return fmt.Errorf("Discord startup: %w", err)
	}
	<-ctx.Done()
	log.Info("shutting down")
	return nil
}
