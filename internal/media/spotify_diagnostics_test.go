package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSpotifyProductionDiagnostics(t *testing.T) {
	for _, failAt := range []string{"", "dns", "bootstrap", "hydrate"} {
		t.Run(failAt, func(t *testing.T) {
			f := newAnonymousFixture(123, 100)
			s := f.client(t)
			var logs bytes.Buffer
			s.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			base := s.Client.Transport
			s.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if failAt == "dns" && r.URL.Host == "spclient.wg.spotify.com" {
					return nil, &net.DNSError{Err: "secret-sentinel", Name: "secret-sentinel", IsNotFound: true}
				}
				if failAt == "hydrate" && strings.HasPrefix(r.URL.Path, "/embed/track/") {
					return jsonResponse(429, "secret-sentinel"), nil
				}
				return base.RoundTrip(r)
			})
			if failAt == "bootstrap" {
				f.session()["accessToken"] = ""
			}
			// Same collection orchestration used by /play, stopping downstream network IO.
			resolver := CollectionResolver{Spotify: s, Search: func(context.Context, string) (Track, error) { return Track{URL: "metadata-only://matched"}, nil }}
			result, err := resolver.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
			if failAt == "" {
				if err != nil || len(result.Tracks) != 123 {
					t.Fatalf("resolved=%d err=%v", len(result.Tracks), err)
				}
				for i, track := range result.Tracks {
					if track.Title != fmt.Sprintf("Track %03d", i+1) {
						t.Fatalf("order mismatch at %d", i)
					}
				}
			} else if err == nil || len(result.Tracks) != 0 {
				t.Fatal("incomplete metadata escaped")
			}
			text := logs.String()
			for _, secret := range []string{"secret-sentinel", "operator-secret", "operator-token", "Bearer", "Authorization", "accessToken", "Cookie"} {
				if strings.Contains(text, secret) {
					t.Fatalf("logs contain sensitive field %q", secret)
				}
			}
			stages := map[string]bool{}
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				var event map[string]any
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				stage, _ := event["stage"].(string)
				stages[stage] = true
				if failAt == "dns" && stage == "spotify_spclient_page_failed" && event["reason"] != "dns_no_address" {
					t.Fatalf("lost DNS cause: %v", event)
				}
			}
			required := []string{"spotify_public_page_complete", "spotify_embed_complete", "spotify_anonymous_bootstrap_start"}
			switch failAt {
			case "":
				required = append(required, "spotify_anonymous_bootstrap_complete", "spotify_spclient_page_start", "spotify_spclient_page_complete", "spotify_tail_enrichment_start", "spotify_tail_enrichment_complete", "spotify_public_complete")
			case "dns":
				required = append(required, "spotify_spclient_page_failed", "spotify_http_failed")
			case "bootstrap":
				required = append(required, "spotify_anonymous_bootstrap_failed")
			case "hydrate":
				required = append(required, "spotify_tail_enrichment_failed", "spotify_http_failed")
			}
			for _, stage := range required {
				if !stages[stage] {
					t.Errorf("missing stage %s", stage)
				}
			}
			if failAt != "" && stages["spotify_public_complete"] {
				t.Fatal("logged false completion")
			}
		})
	}
}

func TestSpotifySafeTransportCause(t *testing.T) {
	original := &url.Error{Op: "Get", URL: "secret-sentinel", Err: &net.DNSError{Err: "secret-sentinel", Name: "secret-sentinel", IsNotFound: true}}
	safe := safeSpotifyFetchFailure(original)
	if !errors.Is(safe, ErrSpotifyPageFetch) || spotifyErrorCategory(safe) != "dns_no_address" || strings.Contains(safe.Error(), "secret-sentinel") {
		t.Fatal("unsafe or unclassified error")
	}
}
