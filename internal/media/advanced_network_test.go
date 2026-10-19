package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Real local TCP and HTTP parsing. All provider authorities are rewritten to
// this private test server; no request can reach a production service.
func TestAdvancedSpotifyNetworkFaults(t *testing.T) {
	for _, fault := range []string{"rate limited", "connection lost", "truncated JSON", "request canceled"} {
		t.Run(fault, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/token" {
					fmt.Fprint(w, `{"access_token":"fixture","expires_in":3600}`)
					return
				}
				entered <- struct{}{}
				switch fault {
				case "rate limited":
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(429)
					fmt.Fprint(w, `{"message":"limited"}`)
				case "connection lost":
					c, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					c.Close()
				case "truncated JSON":
					w.Header().Set("Content-Length", "1000")
					fmt.Fprint(w, `{"id":"1234567890123456789012","name":"partial"`)
				case "request canceled":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			local, _ := url.Parse(server.URL)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "accounts.spotify.com" && r.URL.Host != "api.spotify.com" {
					return nil, fmt.Errorf("unexpected provider authority")
				}
				clone := r.Clone(r.Context())
				u := *r.URL
				u.Host = local.Host
				u.Scheme = local.Scheme
				clone.URL = &u
				return transport.RoundTrip(clone)
			})}
			s := &Spotify{ClientID: "fixture", ClientSecret: "fixture", Client: client}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				collection Collection
				err        error
			}
			done := make(chan result, 1)
			go func() { c, err := s.Collection(ctx, "track", "1234567890123456789012"); done <- result{c, err} }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("request did not reach local fault")
			}
			if fault == "request canceled" {
				cancel()
			}
			select {
			case got := <-done:
				if got.err == nil || len(got.collection.Tracks) != 0 {
					t.Fatalf("partial provider data escaped: %+v %v", got.collection, got.err)
				}
				if fault == "rate limited" && !strings.Contains(got.err.Error(), "429") {
					t.Fatal("rate limit classification lost", got.err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("network failure leaked request")
			}
		})
	}
}
func TestAdvancedIncompleteExtractorMetadata(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"truncated", `{"id":"abcdefghijk"`},
		{"missing video ID", `{"title":"partial"}`},
		{"different video", `{"id":"lmnopqrstuv","title":"wrong"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := resolverFixture(t, tc.body)
			tr, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk")
			if err == nil || tr.URL != "" || tr.VideoID != "" {
				t.Fatalf("incomplete or substituted metadata accepted: %+v err=%v", tr, err)
			}
		})
	}
}
