package discord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestQueueHangulPosition71Preserved(t *testing.T) {
	entries := make([]player.QueueEntry, 71)
	entries[70].Track = media.Track{Title: "희망", Artist: "박세아", Duration: 258234 * time.Millisecond, SpotifyArtworkURL: "https://i.scdn.co/image/cover"}
	from, to := 71, 71
	view, err := buildQueueRange(player.State{Entries: entries}, nil, &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view.Blocks[0].update(""))
	if err != nil || !utf8.Valid(raw) {
		t.Fatal(string(raw), err)
	}
	for _, want := range []string{"희망", "박세아", "04:18", "71 ·"} {
		if !strings.Contains(string(raw), want) {
			t.Fatal(string(raw), want)
		}
	}
	if strings.ContainsRune(string(raw), '\ufffd') {
		t.Fatal("replacement character")
	}
}
