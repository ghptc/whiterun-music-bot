package media

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestEnduranceProviderDNSFailureIsNotPartialSuccess(t *testing.T) {
	failure := &net.DNSError{Err: "QA injected DNS failure", Name: "accounts.spotify.com", IsNotFound: true}
	transport := &http.Transport{Proxy: nil, DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, failure }}
	defer transport.CloseIdleConnections()
	s := &Spotify{ClientID: "fixture", ClientSecret: "fixture", Client: &http.Client{Transport: transport}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	collection, err := s.Collection(ctx, "track", "1234567890123456789012")
	if err == nil || len(collection.Tracks) != 0 {
		t.Fatal("DNS failure published a partial collection", collection, err)
	}
	// Classification must remain visible through the application's error wrapping.
	var dns *net.DNSError
	if !errors.As(err, &dns) {
		t.Fatalf("DNS cause lost: %v", err)
	}
}
