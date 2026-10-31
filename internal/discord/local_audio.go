package discord

import (
	"errors"
	"os"
	"path/filepath"
)

// ConfigureLocalAudio is an operator-only startup configuration. It doesn't add
// a Discord command or change the resolver; an empty directory disables local IO.
// Call before starting any playback, just like ConfigureSpotify/ConfigureStore.
func (b *Bot) ConfigureLocalAudio(directory string) error {
	if directory == "" {
		b.streamer.LocalAudioDir = ""
		return nil
	}
	if !filepath.IsAbs(directory) {
		return errors.New("local audio root must be absolute")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("local audio root unavailable")
	}
	root.Close()
	b.streamer.LocalAudioDir = directory
	return nil
}
