package discord

import (
	"encoding/json"
	"strings"
	"testing"

	d "github.com/disgoorg/disgo/discord"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestQueueNativeIndividualArtwork(t *testing.T) {
	tracks := []media.Track{
		{Title: "Spotify", Artist: "One", SpotifyArtworkURL: "https://i.scdn.co/image/persisted", YouTubeThumbnailURL: "https://i.ytimg.com/other"},
		{Title: "YouTube", Artist: "Two", SpotifyArtworkURL: "invalid", YouTubeThumbnailURL: "https://i.ytimg.com/individual"},
		{Title: "Video", Artist: "Three", VideoID: "abcdefghijk"},
		{Title: "No art", Artist: "Four"},
	}
	state := player.State{}
	for _, track := range tracks {
		state.Entries = append(state.Entries, player.QueueEntry{Track: track})
	}
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	container := view.Blocks[0].Container
	for i, track := range tracks {
		row := container.Components[i+1]
		if media.ArtworkURL(track) == "" {
			if _, ok := row.(d.TextDisplayComponent); !ok {
				t.Fatal("placeholder for absent art")
			}
		} else {
			section, ok := row.(d.SectionComponent)
			if !ok || section.Accessory.(d.ThumbnailComponent).Media.URL != media.ArtworkURL(track) {
				t.Fatalf("wrong individual art at %d", i)
			}
		}
	}
	raw, err := json.Marshal(view.Blocks[0].update(""))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"attachment://", ".png", ".html", ".txt", "media_gallery", "https://i.ytimg.com/other"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("unexpected generated media/fallback", string(raw))
		}
	}
}
