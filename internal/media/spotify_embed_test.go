package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
)

func embedFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	b, err := os.ReadFile("testdata/spotify_embed.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	e := d["props"].(map[string]any)["pageProps"].(map[string]any)["state"].(map[string]any)["data"].(map[string]any)["entity"].(map[string]any)
	return d, e
}
func embedHTML(d map[string]any) string {
	b, _ := json.Marshal(d)
	return `<script id="__NEXT_DATA__" type="application/json">` + string(b) + `</script>`
}
func embedEvidence(e map[string]any) playlistEvidence {
	evidence := playlistEvidence{Total: 50}
	for _, t := range e["trackList"].([]any)[:30] {
		evidence.Prefix = append(evidence.Prefix, t.(map[string]any)["uri"].(string))
	}
	return evidence
}
func evidencePage(e playlistEvidence, next any) string {
	items := make([]any, len(e.Prefix))
	for i, uri := range e.Prefix {
		items[i] = map[string]any{"itemV2": map[string]any{"data": map[string]any{"uri": uri}}}
	}
	d := map[string]any{"entities": map[string]any{"items": map[string]any{"spotify:playlist:" + publicID: map[string]any{"content": map[string]any{"totalCount": e.Total, "items": items, "pagingInfo": map[string]any{"nextOffset": next}}}}}}
	b, _ := json.Marshal(d)
	return `<script id="initialState">` + base64.StdEncoding.EncodeToString(b) + `</script>`
}
func TestSpotifyEmbedComplete(t *testing.T) {
	d, e := embedFixture(t)
	got, err := parseSpotifyEmbed([]byte(embedHTML(d)), publicID, embedEvidence(e))
	if err != nil || len(got.Tracks) != 50 {
		t.Fatal(len(got.Tracks), err)
	}
	for i, raw := range e["trackList"].([]any) {
		entry := raw.(map[string]any)
		if got.Tracks[i].Title != entry["title"] || got.Tracks[i].SourceURL != "https://open.spotify.com/track/"+strings.TrimPrefix(entry["uri"].(string), "spotify:track:") {
			t.Fatalf("order differs at %d", i)
		}
	}
	if len(got.Tracks[0].Artists) != 2 || got.Tracks[0].Artists[0] != "Dr. Dre" {
		t.Fatal(got.Tracks[0])
	}
}
func TestSpotifyEmbedValidation(t *testing.T) {
	for _, name := range []string{"truncated", "total mismatch", "prefix mismatch", "empty", "duplicate occurrence", "missing uid", "wrong identity", "malformed track", "missing artist", "wrong type", "oversize", "duplicate track id", "artist comma"} {
		t.Run(name, func(t *testing.T) {
			d, e := embedFixture(t)
			evidence := embedEvidence(e)
			tracks := e["trackList"].([]any)
			last := tracks[49].(map[string]any)
			switch name {
			case "truncated":
				e["trackList"] = tracks[:30]
			case "total mismatch":
				evidence.Total = 51
			case "prefix mismatch":
				evidence.Prefix[0] = evidence.Prefix[1]
			case "empty":
				e["trackList"] = []any{}
			case "duplicate occurrence":
				last["uid"] = tracks[0].(map[string]any)["uid"]
			case "missing uid":
				delete(last, "uid")
			case "wrong identity":
				e["uri"] = "spotify:playlist:1234567890123456789012"
			case "malformed track":
				last["uri"] = "spotify:track:invalid"
			case "missing artist":
				last["subtitle"] = " "
			case "wrong type":
				last["entityType"] = "episode"
			case "oversize":
				evidence.Total = MaxCollectionTracks + 1
			case "duplicate track id":
				last["uri"] = tracks[0].(map[string]any)["uri"]
			case "artist comma":
				last["subtitle"] = "Artist, With Comma,\u00a0Second Artist"
			}
			got, err := parseSpotifyEmbed([]byte(embedHTML(d)), publicID, evidence)
			if name == "duplicate track id" || name == "artist comma" {
				if err != nil || len(got.Tracks) != 50 {
					t.Fatal(err)
				}
				if name == "artist comma" && (len(got.Tracks[49].Artists) != 2 || got.Tracks[49].Artists[0] != "Artist, With Comma") {
					t.Fatal(got.Tracks[49])
				}
			} else if err == nil || len(got.Tracks) != 0 {
				t.Fatalf("accepted unsafe snapshot: tracks=%d err=%v", len(got.Tracks), err)
			}
		})
	}
	d, e := embedFixture(t)
	for _, body := range []string{"broken", `<script id="__NEXT_DATA__">{"accessToken":"secret-sentinel",</script>`, embedHTML(d) + embedHTML(d)} {
		_, err := parseSpotifyEmbed([]byte(body), publicID, embedEvidence(e))
		if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("invalid bootstrap accepted or leaked")
		}
	}
}
func TestSpotifyEmbedEvidence(t *testing.T) {
	_, e := embedFixture(t)
	ev := embedEvidence(e)
	for _, next := range []any{nil, 0, 29, 31, 30} {
		got, err := spotifyPlaylistEvidence([]byte(evidencePage(ev, next)), publicID)
		if next == 30 {
			if err != nil || got.Total != 50 || len(got.Prefix) != 30 {
				t.Fatal(got, err)
			}
		} else if err == nil {
			t.Fatal("non-advancing prefix accepted")
		}
	}
	body := evidencePage(ev, 30)
	if _, err := spotifyPlaylistEvidence([]byte(body+body), publicID); err == nil {
		t.Fatal("ambiguous state accepted")
	}
}
func embedSpotify(t *testing.T, body string, status int) *Spotify {
	t.Helper()
	_, e := embedFixture(t)
	return &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "accounts.spotify.com":
			return jsonResponse(200, `{"access_token":"token","expires_in":3600}`), nil
		case "api.spotify.com":
			return jsonResponse(404, ""), nil
		case "open.spotify.com":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("credentials sent to public page")
			}
			if strings.HasPrefix(r.URL.Path, "/embed/") {
				return jsonResponse(status, body), nil
			}
			return jsonResponse(200, evidencePage(embedEvidence(e), 30)), nil
		}
		return nil, fmt.Errorf("unexpected host")
	})}}
}
func TestSpotifyEmbedFallbackAndOrdering(t *testing.T) {
	d, e := embedFixture(t)
	s := embedSpotify(t, embedHTML(d), 200)
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://open.spotify.com")
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret-sentinel"}})
	s.Client.Jar = jar
	r := CollectionResolver{Spotify: s, Search: func(ctx context.Context, q string) (Track, error) {
		return Track{URL: "https://www.youtube.com/watch?v=correct0001"}, nil
	}}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err != nil || len(got.Tracks) != 50 {
		t.Fatal(len(got.Tracks), err)
	}
	for i, raw := range e["trackList"].([]any) {
		if got.Tracks[i].Title != raw.(map[string]any)["title"] {
			t.Fatalf("order lost at %d", i)
		}
	}
	for _, status := range []int{200, 403, 429, 500} {
		r.Spotify = embedSpotify(t, `<script id="__NEXT_DATA__">{"accessToken":"secret-sentinel"}</script>`, status)
		r.Search = func(context.Context, string) (Track, error) {
			t.Fatal("incomplete fallback reached matching")
			return Track{}, nil
		}
		got, err = r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
		if err == nil || len(got.Tracks) != 0 || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("partial result or unsafe error")
		}
	}
}
func TestSpotifyEmbedCancellation(t *testing.T) {
	d, _ := embedFixture(t)
	s := embedSpotify(t, embedHTML(d), 200)
	base := s.Client.Transport
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/embed/") {
			cancel()
			return nil, r.Context().Err()
		}
		return base.RoundTrip(r)
	})
	got, err := s.Collection(ctx, "playlist", publicID)
	if !errors.Is(err, context.Canceled) || len(got.Tracks) != 0 {
		t.Fatal(got, err)
	}
}

// Explicit opt-in metadata-only check; the normal suite remains entirely offline.
func TestSpotifyEmbedLiveRegression(t *testing.T) { liveSpotifyRegression(t, publicID, 50) }
func liveSpotifyRegression(t *testing.T, id string, total int) {
	t.Helper()
	if os.Getenv("BARD_SPOTIFY_LIVE_TEST") != "1" {
		t.Skip("set BARD_SPOTIFY_LIVE_TEST=1 for public metadata verification")
	}
	s := &Spotify{}
	got, err := s.publicPlaylist(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != total {
		t.Fatalf("resolved=%d expected=%d", len(got.Tracks), total)
	}
	t.Logf("playlist=%s resolved=%d total=%d first=%q last=%q", id, len(got.Tracks), total, got.Tracks[0].Title, got.Tracks[total-1].Title)
}
