package player

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"whiterun/internal/media"
)

type spotifyTransport func(*http.Request) (*http.Response, error)

func (f spotifyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicSpotifyOrderedAtomicQueue(t *testing.T) {
	body, err := os.ReadFile("../media/testdata/spotify_public.html")
	if err != nil {
		t.Fatal(err)
	}
	state := `{"entities":{"items":{"spotify:playlist:37i9dQZF1DZ06evO3VkoW4":{"content":{"items":[{},{}],"totalCount":2,"pagingInfo":{"nextOffset":null}}}}}}`
	page := string(body) + "<script id=initialState>" + base64.StdEncoding.EncodeToString([]byte(state)) + "</script>"
	spotify := &media.Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: spotifyTransport(func(r *http.Request) (*http.Response, error) {
		status, text := 200, page
		switch r.URL.Host {
		case "accounts.spotify.com":
			text = `{"access_token":"token","expires_in":3600}`
		case "api.spotify.com":
			status, text = 404, ""
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: make(http.Header)}, nil
	})}}
	started := make(chan string, 2)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	r := media.CollectionResolver{Spotify: spotify, Search: func(ctx context.Context, q string) (media.Track, error) {
		if strings.Contains(q, "Still D.R.E.") {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
		}
		return media.Track{URL: "https://www.youtube.com/watch?v=correct0001"}, nil
	}}
	ticket := p.Ticket()
	done := make(chan error, 1)
	go func() {
		collection, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/37i9dQZF1DZ06evO3VkoW4")
		if err == nil {
			err = p.EnqueueMany(collection.Tracks, ticket)
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("matching did not start")
	}
	current, queue := p.Snapshot()
	if current != nil || len(queue) != 0 {
		t.Fatal("partial insertion")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	receive(t, started, "Still D.R.E.")
	current, queue = p.Snapshot()
	if current == nil || len(queue) != 1 || queue[0].Title != "The Next Episode" || len(queue[0].Artists) != 2 {
		t.Fatal(current, queue)
	}
}
