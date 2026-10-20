//go:build stress

package qasoak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestEnduranceGuildFootprint(t *testing.T) {
	for _, count := range []int{1, 2, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			runtime.GC()
			start := time.Now()
			baseline := resources("baseline", start)
			var players []*player.GuildPlayer
			for g := 0; g < count; g++ {
				p := player.New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, _ player.Voice) error { <-ctx.Done(); return ctx.Err() })
				players = append(players, p)
				if err := p.Attach(recoveryVoice{}); err != nil {
					t.Fatal(err)
				}
				tracks := make([]media.Track, 500)
				for i := range tracks {
					tracks[i] = media.Track{Title: fmt.Sprintf("guild%d/%d", g, i), SpotifyID: fmt.Sprint(i), Artists: []string{"artist"}}
				}
				if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
					t.Fatal(err)
				}
			}
			runtime.GC()
			loaded := resources("loaded", start)
			for _, p := range players {
				p.Close(context.Background())
			}
			players = nil
			runtime.GC()
			after := resources("after_cleanup", start)
			b, err := json.Marshal(map[string]any{"guilds": count, "tracks_per_guild": 500, "baseline": baseline, "loaded": loaded, "after_cleanup": after})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("FOOTPRINT %s", b)
		})
	}
}
