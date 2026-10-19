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
		row := container.Components[2*i+1]
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

func TestQueueCompactContinuations(t *testing.T) {
	for _, n := range []int{1, 8, 9, 10, 18, 20, 123, 500} {
		state := queueFixture(n)
		state.Current = &media.Track{Title: "Current", Artist: "Artist", SpotifyArtworkURL: "https://art.example/current?size=48"}
		view, err := buildQueueView(state, nil)
		if err != nil {
			t.Fatal(err)
		}
		text := viewText(view)
		if strings.Count(text, "The Bard · Queue") != 1 || strings.Count(text, "songs up next") != 1 || strings.Count(text, "Now playing · **Current**") != 1 {
			t.Fatalf("repeated heading/count/current for %d: %s", n, text)
		}
		for i, block := range view.Blocks {
			assertV2Budget(t, block.Container)
			start := 0
			if i == 0 {
				start = 1
			}
			if len(block.Container.Components) != start+2*len(block.Rows) || block.Container.AccentColor != whiterunAccent {
				t.Fatal("incorrect separators or inconsistent accent")
			}
			for j, row := range block.Rows {
				section := block.Container.Components[start+2*j].(d.SectionComponent)
				if j > 0 {
					separator, ok := block.Container.Components[start+2*j-1].(d.SeparatorComponent)
					if !ok || separator.Spacing != d.SeparatorSpacingSizeSmall || (separator.Divider != nil && !*separator.Divider) {
						t.Fatal("expected a small divider only between songs")
					}
				}
				label := section.Components[0].(d.TextDisplayComponent).Content
				if !strings.HasPrefix(label, "### ") || strings.Contains(label, "\n-# ") {
					t.Fatal("queue needs a small heading and normal-size artist", label)
				}
				if len(section.Components) != 1 || strings.Count(label, "\n") != 1 || strings.Contains(label, "\n\n") {
					t.Fatal("row must contain only title and artist", label)
				}
				if section.Accessory.(d.ThumbnailComponent).Media.URL != media.ArtworkURL(row.Track) {
					t.Fatal("artwork URL was rewritten")
				}
			}
			footer := block.Container.Components[len(block.Container.Components)-1].(d.TextDisplayComponent).Content
			if !strings.HasPrefix(footer, "-# "+block.rangeLabel()) {
				t.Fatal("missing discreet position range", footer)
			}
		}
	}
}
