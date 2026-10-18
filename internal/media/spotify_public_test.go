package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const publicID = "37i9dQZF1DZ06evO3VkoW4"

func publicFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/spotify_public.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func withCount(body string, count int, next any) string {
	items := make([]any, count)
	state := map[string]any{"entities": map[string]any{"items": map[string]any{"spotify:playlist:" + publicID: map[string]any{"name": "Playlist", "content": map[string]any{"totalCount": count, "items": items, "pagingInfo": map[string]any{"nextOffset": next}}}}}}
	b, _ := json.Marshal(state)
	return body + "<script id=initialState type=text/plain>" + base64.StdEncoding.EncodeToString(b) + "</script>"
}
func TestSpotifyPublicParser(t *testing.T) {
	fixture := publicFixture(t)
	first := fixture[:strings.Index(fixture, "</div>")+6]
	for _, tt := range []struct {
		name, body string
		count      int
		want       error
	}{
		{"multiple explicit", fixture, 2, nil},
		{"one", first, 1, nil},
		{"entities", strings.ReplaceAll(first, "Still D.R.E.", "L’été &amp; &lt;夜&gt;"), 1, nil},
		{"duplicate IDs", first + first, 2, nil},
		{"missing artist", strings.ReplaceAll(first, "/artist/", "/other/"), 1, ErrSpotifyPageParse},
		{"bad ID", strings.ReplaceAll(first, "503OTo2dSqe7qk76rgsbep", "bad"), 1, ErrSpotifyPageParse},
		{"empty", "<html></html>", 0, ErrNoTracks},
		{"truncated", fixture, 50, ErrSpotifyIncomplete},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSpotifyPlaylist([]byte(withCount(tt.body, tt.count, nil)), publicID)
			if !errors.Is(err, tt.want) {
				t.Fatalf("%+v %v", got, err)
			}
			if err != nil {
				if len(got.Tracks) != 0 {
					t.Fatal("partial result")
				}
				return
			}
			if len(got.Tracks) != tt.count {
				t.Fatal(got)
			}
			for _, track := range got.Tracks {
				if len(track.Artists) != 2 || track.Artists[1] != "Snoop Dogg" || track.Artist != "Dr. Dre" {
					t.Fatal(track)
				}
			}
			if tt.name == "entities" && got.Tracks[0].Title != "L’été & <夜>" {
				t.Fatal(got)
			}
			if tt.count == 2 && tt.name != "duplicate IDs" && got.Tracks[1].Title != "The Next Episode" {
				t.Fatal(got)
			}
		})
	}
	for _, body := range []string{fixture, withCount(fixture, 2, 2), withCount(fixture, 2, nil)} {
		id := publicID
		if body == withCount(fixture, 2, nil) {
			id = "1234567890123456789012"
		}
		_, err := parseSpotifyPlaylist([]byte(body), id)
		if !errors.Is(err, ErrSpotifyIncomplete) {
			t.Fatal(err)
		}
	}
}
func publicSpotify(status int, body string, calls *int) *Spotify {
	return &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "accounts.spotify.com":
			return jsonResponse(200, `{"access_token":"token","expires_in":3600}`), nil
		case "api.spotify.com":
			if status != 200 {
				return jsonResponse(status, ""), nil
			}
			if strings.HasSuffix(r.URL.Path, "/items") {
				return jsonResponse(200, `{"items":[{"item":`+spotifyTrackJSON+`}],"next":null}`), nil
			}
			return jsonResponse(200, spotifyTrackJSON), nil
		case "open.spotify.com":
			*calls++
			if r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") == "" {
				return nil, errors.New("bad public headers")
			}
			return jsonResponse(200, body), nil
		}
		return nil, errors.New("unexpected host")
	})}}
}
func TestSpotifyPublicFallbackConditions(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := publicSpotify(status, withCount(publicFixture(t), 2, nil), &calls)
			_, err := s.Collection(context.Background(), "playlist", publicID)
			eligible := status == 403 || status == 404
			if (calls == 1) != eligible {
				t.Fatal(calls, err)
			}
			if (status == 200 || eligible) && err != nil {
				t.Fatal(err)
			}
		})
	}
	calls := 0
	s := publicSpotify(404, "invalid page", &calls)
	_, err := s.Collection(context.Background(), "playlist", publicID)
	if !errors.Is(err, ErrSpotifyAccess) || !errors.Is(err, ErrSpotifyIncomplete) || !strings.Contains(err.Error(), "status=404") {
		t.Fatal(err)
	}
	for _, kind := range []string{"track", "album", "unsupported"} {
		calls = 0
		_, _ = s.Collection(context.Background(), kind, publicID)
		if calls != 0 {
			t.Fatal(kind, calls)
		}
	}
	s.ClientID = ""
	calls = 0
	_, err = s.Collection(context.Background(), "playlist", publicID)
	if !errors.Is(err, ErrSpotifyConfig) || calls != 0 {
		t.Fatal(err, calls)
	}
}
func TestSpotifyPublicHTTP(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"status", 503, "", ErrSpotifyPageFetch},
		{"oversize", 200, strings.Repeat("x", maxSpotifyPageBytes+1), ErrSpotifyPageFetch},
		{"bad html", 200, "broken", ErrSpotifyIncomplete},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Spotify{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(tt.status, tt.body), nil })}}
			_, err := s.publicPlaylist(context.Background(), publicID)
			if !errors.Is(err, tt.want) {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	s := &Spotify{Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	done := make(chan error, 1)
	go func() { _, err := s.publicPlaylist(ctx, publicID); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}
func TestSpotifyPublicCanonicalResolver(t *testing.T) {
	calls := 0
	s := publicSpotify(404, withCount(publicFixture(t), 2, nil), &calls)
	youtube, _ := candidateResolverFixture(t, `{"entries":[{"id":"correct0001","title":"Still D.R.E.","artists":["Dr. Dre","Snoop Dogg"],"channel":"Dr. Dre - Topic","duration":270},{"id":"correct0002","title":"The Next Episode","artists":["Dr. Dre","Snoop Dogg"],"channel":"Dr. Dre - Topic","duration":161},{"id":"wrong000001","title":"Still D.R.E. live","artist":"Dr. Dre","duration":270}]}`)
	r := CollectionResolver{Spotify: s, YouTube: youtube}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err != nil || len(got.Tracks) != 2 {
		t.Fatal(got, err)
	}
	for i, id := range []string{"correct0001", "correct0002"} {
		if got.Tracks[i].VideoID != id || len(got.Tracks[i].Artists) != 2 || got.Tracks[i].RequestedSource != "spotify" {
			t.Fatal(got)
		}
	}
}

func TestSpotifyObservedTruncationNeverMatches(t *testing.T) {
	body, err := os.ReadFile("testdata/spotify_public_truncated.html")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := publicSpotify(404, string(body), &calls)
	r := CollectionResolver{Spotify: s, Search: func(context.Context, string) (Track, error) {
		t.Fatal("incomplete playlist reached YouTube")
		return Track{}, nil
	}}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if !errors.Is(err, ErrSpotifyIncomplete) || !strings.Contains(err.Error(), "rendered=30 total=50") || len(got.Tracks) != 0 {
		t.Fatal(got, err)
	}
	// If all exposed rows are independently certified as complete, all 30
	// must be extracted, not just the two songs in the minimal fixture.
	rows := string(body[:strings.Index(string(body), "<script")])
	got, err = parseSpotifyPlaylist([]byte(withCount(rows, 30, nil)), publicID)
	if err != nil || len(got.Tracks) != 30 {
		t.Fatal(got, err)
	}
	if got.Tracks[0].Title != "The Next Episode" || got.Tracks[1].Title != "Still D.R.E." {
		t.Fatal(got)
	}
}
