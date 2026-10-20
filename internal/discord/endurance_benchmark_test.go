package discord

import (
	"context"
	"encoding/json"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Measures SDK acknowledgement over real local TCP, not Discord Internet latency.
func BenchmarkEnduranceLocalACK(b *testing.B) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type int `json:"type"`
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/callback") || json.NewDecoder(r.Body).Decode(&body) != nil || body.Type != 5 {
			b.Error("invalid acknowledgement contract")
			w.WriteHeader(400)
			return
		}
		received.Add(1)
		w.WriteHeader(204)
	}))
	defer server.Close()
	local, _ := url.Parse(server.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := rest.NewClient("fixture", rest.WithLogger(log), rest.WithRateLimiter(newManagedRESTLimiter(log)), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = local.Scheme
		u.Host = local.Host
		clone.URL = &u
		return transport.RoundTrip(clone)
	})}))
	defer client.Close(context.Background())
	sender := rest.New(client)
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		err := sender.CreateInteractionResponse(1, "qa", d.InteractionResponse{Type: d.InteractionResponseTypeDeferredCreateMessage}, rest.WithCtx(ctx))
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
	if received.Load() != int64(b.N) {
		b.Fatal("acknowledgements lost or duplicated", received.Load(), b.N)
	}
}
