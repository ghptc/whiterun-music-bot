package media

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
)

func BenchmarkPlaybackMetadata500(b *testing.B) {
	for _, fast := range []bool{false, true} {
		b.Run(fmt.Sprintf("deferred=%v", fast), func(b *testing.B) {
			// Same provider fixture and validation paths, real JSON/HTML parsing.
			// Requests are deterministic in-memory responses, never external latency.
			f := newAnonymousFixture(500, 100)
			s := f.client(b)
			s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				var err error
				if fast {
					_, err = s.CollectionForPlayback(context.Background(), "playlist", publicID)
				} else {
					_, err = s.Collection(context.Background(), "playlist", publicID)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
