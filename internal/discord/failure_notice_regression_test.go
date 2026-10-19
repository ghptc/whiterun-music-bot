package discord

import (
	"strings"
	"testing"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestFailureNoticeIsNotUserSkip(t *testing.T) {
	text := playbackText(player.PlaybackEvent{Track: media.Track{Title: "Little Bad Girl", SpotifyID: "original", Position: 8, Total: 123}, SkippedReason: "Audio unavailable; playback could not complete."})
	if strings.Contains(text, "Skipped:") {
		t.Fatal("failure misreported as user skip:", text)
	}
}
