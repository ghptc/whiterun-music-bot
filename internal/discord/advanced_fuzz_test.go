package discord

import (
	"testing"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func FuzzAdvancedQueueArguments(f *testing.F) {
	f.Add("Song 🎵", 1)
	f.Add("@everyone\x00", 0)
	f.Add("東京", 1000)
	f.Add("malformed\xff", -1)
	f.Fuzz(func(t *testing.T, title string, limit int) {
		if len(title) > 4096 {
			t.Skip()
		}
		tracks := make([]media.Track, 32)
		for i := range tracks {
			tracks[i] = media.Track{Title: title, Artist: "Artist"}
		}
		view, err := buildQueueView(player.State{Queued: tracks}, &limit)
		if limit < 1 {
			if err == nil {
				t.Fatal("invalid command limit accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if view.Shown != min(limit, 32) || view.Total != 32 {
			t.Fatalf("limit lost tracks: shown=%d total=%d", view.Shown, view.Total)
		}
		for _, block := range view.Blocks {
			assertV2Budget(t, block.Container)
		}
	})
}
