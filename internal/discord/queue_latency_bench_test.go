package discord

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Deterministic cold-cache provider latency; no internet or Discord credentials.
func BenchmarkQueueColdArtwork(b *testing.B) {
	state := queueFixture(10)
	for i := range state.Entries {
		state.Entries[i].Track.SpotifyArtworkURL = "https://i.scdn.co/" + state.Entries[i].Track.Title
	}
	b.ReportAllocs()
	for b.Loop() {
		renderer := newQueueImageRenderer()
		renderer.client = &http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		view, _ := buildQueueView(state, nil)
		if err := renderer.render(context.Background(), &view.Blocks[0]); err != nil {
			b.Fatal(err)
		}
	}
}
