package player

import (
	"fmt"
	"testing"
	"whiterun/internal/media"
)

func BenchmarkQualitySnapshot(b *testing.B) {
	for _, size := range []int{1, 500, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			// No run goroutine: measure only snapshot and deep-copy work.
			p := &GuildPlayer{}
			for i := range size {
				p.queue = append(p.queue, queueEntry{Track: media.Track{Title: fmt.Sprint(i), Artists: []string{"artist"}, Explicit: new(true)}})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s := p.State()
				if len(s.Queued) != size {
					b.Fatal("snapshot length")
				}
			}
		})
	}
}
