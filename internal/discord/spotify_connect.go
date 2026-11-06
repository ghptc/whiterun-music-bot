package discord

import (
	"errors"
	"os"
	"path/filepath"
	"whiterun/internal/player"
	"whiterun/internal/spotifyconnect"
)

// ConfigureSpotifyConnect is startup-only, opt-in and uses a dedicated local
// daemon. Authorization to retransmit was confirmed by the operator for
// BARD-SPOTIFY-001; deployments still require separate release verification.
func (b *Bot) ConfigureSpotifyConnect(endpoint, pipe string) error {
	if !filepath.IsAbs(pipe) {
		return errors.New("Spotify FIFO path must be absolute")
	}
	info, err := os.Lstat(pipe)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("Spotify FIFO must be private (0600)")
	}
	parent, err := os.Stat(filepath.Dir(pipe))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return errors.New("Spotify FIFO directory must be private (0700)")
	}
	c, err := spotifyconnect.New(endpoint)
	if err != nil {
		return err
	}
	b.streamer.Spotify = &player.SpotifySession{Client: c, Pipe: pipe}
	b.resolver.NativeSpotify = true
	if b.log != nil {
		b.log.Info("spotify_connect_started", "component", "adapter", "player_version", "v0.10.5")
	}
	return nil
}
