package discord

import (
	"encoding/json"
	"fmt"
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

func TestQueueCompactSingleMessage(t *testing.T) {
	view, err := buildQueueView(queueFixture(1000), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Blocks) != 1 || view.Shown <= 10 || view.Shown >= 1000 {
		t.Fatal(view)
	}
	assertV2Budget(t, view.Blocks[0].Container)
	art := 0
	for _, c := range view.Blocks[0].Container.Components {
		if _, ok := c.(d.SectionComponent); ok {
			art++
		}
	}
	if art != 12 || !strings.Contains(viewText(view), "Next position:") {
		t.Fatal(art, viewText(view))
	}
}

func TestQueueRangesAndWholeSongs(t *testing.T) {
	state := queueFixture(1000)
	for _, tc := range []struct{ from, to, limit, want int }{{20, 30, 100, 11}, {990, 1000, 3, 3}, {1, 1000, 1000, 0}} {
		v, err := buildQueueRange(state, &tc.limit, &tc.from, &tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Blocks) != 1 || v.Shown > tc.to-tc.from+1 {
			t.Fatal(v)
		}
		if tc.want > 0 && v.Shown != tc.want {
			t.Fatal(v.Shown, tc.want)
		}
		for i, row := range v.Blocks[0].Rows {
			if row.Position != tc.from+i {
				t.Fatal(row)
			}
		}
		text := viewText(v)
		if !strings.Contains(text, fmt.Sprintf("%d positions displayed · %d remaining", v.Shown, 1000-v.Shown)) {
			t.Fatal(text)
		}
		if tc.from+v.Shown <= min(tc.to, 1000) && v.Shown < tc.limit && !strings.Contains(text, fmt.Sprintf("Next position: %d", tc.from+v.Shown)) {
			t.Fatal(text)
		}
		assertV2Budget(t, v.Blocks[0].Container)
	}
	for _, args := range [][3]int{{0, 3, 1}, {3, 2, 1}, {1, 3, 0}} {
		if _, err := buildQueueRange(state, &args[2], &args[0], &args[1]); err == nil {
			t.Fatal(args)
		}
	}
	state.Entries[1].Track.Title = strings.Repeat("🎵", 2100)
	v, err := buildQueueView(state, nil)
	if err != nil || v.Shown != 1 || !strings.Contains(viewText(v), "Next position: 2") || strings.Contains(viewText(v), "…") {
		t.Fatal(v.Shown, err, viewText(v))
	}
}
