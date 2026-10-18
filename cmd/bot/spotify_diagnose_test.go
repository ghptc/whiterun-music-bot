package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestConfiguredSpotify(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", " client ")
	t.Setenv("SPOTIFY_CLIENT_SECRET", " secret ")
	t.Setenv("SPOTIFY_REFRESH_TOKEN", " refresh ")
	t.Setenv("SPOTIFY_MARKET", " BR ")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := configuredSpotify(log)
	if s.ClientID != "client" || s.ClientSecret != "secret" || s.RefreshToken != "refresh" || s.Market != "BR" || s.Log != log || s.Client != nil {
		t.Fatal("production constructor configuration changed")
	}
}
