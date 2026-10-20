package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func BenchmarkQA013QueueCommit(b *testing.B) {
	for _, size := range []int{0, 302, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			db, err := Open(filepath.Join(b.TempDir(), "baseline.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			state := player.DurableState{Loop: "off", Queue: make([]media.Track, size)}
			for i := range state.Queue {
				state.Queue[i] = media.Track{Title: fmt.Sprint(i), Position: i + 1, Total: size}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := db.SaveGuild(context.Background(), "1", state); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
