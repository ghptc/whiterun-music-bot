package discord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestREL024CardsPreserveMetadataWithoutBrandPrefix(t *testing.T) {
	track := media.Track{Title: "A música — Live", Artist: "Artista", Duration: 123 * time.Second, SpotifyArtworkURL: "https://i.scdn.co/image/cover"}
	state := player.State{Current: &track, CurrentStarted: true}
	card := nowContainer(state)
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"### Now Playing", "A música — Live", "Artista", "02:03", "https://i.scdn.co/image/cover"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "The Bard") {
		t.Fatal(text)
	}
	state.Entries = []player.QueueEntry{{Track: track}}
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(view.Blocks[0].Container)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "### Queue") || strings.Contains(string(data), "The Bard") {
		t.Fatal(string(data))
	}
}
