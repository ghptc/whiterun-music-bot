package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
	"whiterun/internal/media"
)

// Runs the /play collection orchestration and production Spotify constructor.
// Only downstream YouTube matching is substituted; no gateway/audio/queue is opened.
func diagnoseSpotify(log *slog.Logger, urls []string) error {
	if len(urls) == 0 {
		return fmt.Errorf("spotify-diagnose requires playlist URLs")
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return err
	}
	spotify := configuredSpotify(log)
	log.Info("Spotify production diagnostic", "binary_sha256", fmt.Sprintf("%x", hash.Sum(nil)), "dns_edns_disabled", strings.Contains(os.Getenv("GODEBUG"), "netedns0=0"), "market", spotify.Market, "credentials_configured", spotify.ClientID != "" && spotify.ClientSecret != "", "refresh_configured", spotify.RefreshToken != "")
	resolver := media.CollectionResolver{Spotify: spotify, Log: log, Search: func(context.Context, string) (media.Track, error) {
		return media.Track{URL: "metadata-only://matched"}, nil
	}}
	for _, query := range urls {
		ctx, cancel := context.WithCancel(context.Background())
		start := time.Now()
		result, err := resolver.Resolve(ctx, query)
		cancel()
		if err != nil {
			return err
		}
		log.Info("Spotify production diagnostic complete", "resolved", len(result.Tracks), "elapsed_ms", time.Since(start).Milliseconds())
	}
	return nil
}
