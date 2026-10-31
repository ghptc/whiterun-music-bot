package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"whiterun/internal/discord"
	"whiterun/internal/media"
)

func configureYouTubeEfficiency(b *discord.Bot) error {
	c := media.DefaultProviderAdmissionConfig()
	horizon := media.VerificationPrefetchHorizon
	if raw := os.Getenv("YOUTUBE_MAX_CONCURRENT"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid YOUTUBE_MAX_CONCURRENT")
		}
		c.Concurrency = v
	}
	if raw := os.Getenv("YOUTUBE_ADMISSION_INTERVAL"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil || v < time.Millisecond {
			return fmt.Errorf("YOUTUBE_ADMISSION_INTERVAL must be at least 1ms")
		}
		c.Interval = v
	}
	if raw := os.Getenv("YOUTUBE_PREFETCH_HORIZON"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid YOUTUBE_PREFETCH_HORIZON")
		}
		horizon = v
	}
	return b.ConfigureYouTubeEfficiency(c, horizon)
}
