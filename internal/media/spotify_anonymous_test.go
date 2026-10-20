package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type anonymousFixture struct {
	mu       sync.Mutex
	uris     []string
	embedded int
	envelope map[string]any
	evidence playlistEvidence
	calls    map[string]int
	mutate   func(*http.Request, map[string]any) (int, string)
}

func newAnonymousFixture(total, embedded int) *anonymousFixture {
	f := &anonymousFixture{embedded: embedded, calls: map[string]int{}, evidence: playlistEvidence{Total: total}}
	entries := make([]any, embedded)
	for i := range total {
		uri := fmt.Sprintf("spotify:track:%022d", i+1)
		f.uris = append(f.uris, uri)
		if i < min(30, total) {
			f.evidence.Prefix = append(f.evidence.Prefix, uri)
		}
		if i < embedded {
			entries[i] = map[string]any{"uri": uri, "uid": fmt.Sprint(i), "title": fmt.Sprintf("Track %03d", i+1), "subtitle": "Artist,\u00a0Guest", "entityType": "track", "duration": 120000}
		}
	}
	f.envelope = map[string]any{"props": map[string]any{"pageProps": map[string]any{"state": map[string]any{
		"data":     map[string]any{"entity": map[string]any{"type": "playlist", "uri": "spotify:playlist:" + publicID, "name": "Fixture", "trackList": entries}},
		"settings": map[string]any{"session": map[string]any{"accessToken": "anonymous-secret-sentinel", "isAnonymous": true, "accessTokenExpirationTimestampMs": time.Now().Add(time.Hour).UnixMilli()}},
	}}}}
	return f
}
func (f *anonymousFixture) session() map[string]any {
	return f.envelope["props"].(map[string]any)["pageProps"].(map[string]any)["state"].(map[string]any)["settings"].(map[string]any)["session"].(map[string]any)
}
func (f *anonymousFixture) client(t testing.TB) *Spotify {
	t.Helper()
	return &Spotify{ClientID: "id", ClientSecret: "operator-secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.URL.Host + r.URL.Path + "?" + r.URL.RawQuery
		f.calls[key]++
		var data map[string]any
		switch r.URL.Host {
		case "accounts.spotify.com":
			return jsonResponse(200, `{"access_token":"operator-token","expires_in":3600}`), nil
		case "api.spotify.com":
			return jsonResponse(404, ""), nil
		case "open.spotify.com":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("public request carried credentials")
			}
			if strings.HasPrefix(r.URL.Path, "/embed/playlist/") {
				data = f.envelope
			} else if strings.HasPrefix(r.URL.Path, "/embed/track/") {
				id := strings.TrimPrefix(r.URL.Path, "/embed/track/")
				n, _ := strconv.Atoi(id)
				data = map[string]any{"props": map[string]any{"pageProps": map[string]any{"state": map[string]any{"data": map[string]any{"entity": map[string]any{"type": "track", "uri": "spotify:track:" + id, "title": fmt.Sprintf("Track %03d", n), "duration": 120000, "artists": []any{map[string]any{"name": "Artist"}, map[string]any{"name": "Guest"}}}}}}}}
			} else {
				return jsonResponse(200, evidencePage(f.evidence, min(30, len(f.uris)))), nil
			}
		case "spclient.wg.spotify.com":
			if r.Header.Get("Authorization") != "Bearer anonymous-secret-sentinel" && r.Header.Get("Authorization") != "Bearer refreshed-secret-sentinel" {
				t.Error("wrong anonymous credentials")
			}
			if r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" {
				t.Error("incorrect anonymous headers")
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("from"))
			items := []any{}
			for i := offset; i < min(offset+50, len(f.uris)); i++ {
				items = append(items, map[string]any{"uri": f.uris[i], "attributes": map[string]any{"itemId": fmt.Sprintf("occurrence-%d", i)}})
			}
			data = map[string]any{"revision": "stable-revision", "length": len(f.uris), "contents": map[string]any{"pos": offset, "truncated": offset+len(items) < len(f.uris), "items": items}}
		default:
			t.Error("unexpected destination")
			return nil, errors.New("unexpected destination")
		}
		if f.mutate != nil {
			status, body := f.mutate(r, data)
			if status != 0 {
				return jsonResponse(status, body), nil
			}
		}
		b, _ := json.Marshal(data)
		if r.URL.Host == "open.spotify.com" {
			return jsonResponse(200, `<script id="__NEXT_DATA__">`+string(b)+`</script>`), nil
		}
		return jsonResponse(200, string(b)), nil
	})}}
}
func TestAnonymousCompletePagination(t *testing.T) {
	for _, total := range []int{50, 123, 150} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			f := newAnonymousFixture(total, min(30, total))
			s := f.client(t)
			got, err := s.Collection(context.Background(), "playlist", publicID)
			if err != nil || len(got.Tracks) != total {
				t.Fatalf("resolved=%d err=%v", len(got.Tracks), err)
			}
			for i, track := range got.Tracks {
				if track.Title != fmt.Sprintf("Track %03d", i+1) || len(track.Artists) != 2 {
					t.Fatalf("wrong metadata/order at %d", i)
				}
			}
			pages := 0
			for path, n := range f.calls {
				if strings.HasPrefix(path, "spclient.") {
					pages += n
				}
			}
			if pages != (total+49)/50+1 {
				t.Fatal("unbounded or missing pages", pages)
			}
		})
	}
}
func TestAnonymousDuplicateEntriesAndCollectionOrder(t *testing.T) {
	f := newAnonymousFixture(123, 100)
	f.uris[119] = f.uris[109] // repeated unknown metadata: fetched once, emitted twice
	f.uris[121] = f.uris[0]   // repeated already cached metadata
	r := CollectionResolver{Spotify: f.client(t), Search: func(context.Context, string) (Track, error) {
		return Track{URL: "https://youtube.com/watch?v=correct0001"}, nil
	}}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err != nil || len(got.Tracks) != 123 {
		t.Fatal(len(got.Tracks), err)
	}
	for i, uri := range f.uris {
		if got.Tracks[i].SourceURL != "https://open.spotify.com/track/"+strings.TrimPrefix(uri, "spotify:track:") {
			t.Fatalf("wrong position %d", i)
		}
	}
	if got.Tracks[119].Title != "Track 110" || got.Tracks[121].Title != "Track 001" {
		t.Fatal("duplicates lost")
	}
	if f.calls["open.spotify.com/embed/track/0000000000000000000110?"] != 1 {
		t.Fatal("metadata not reused")
	}
}
func TestAnonymousRejectsIncompleteAndMalformed(t *testing.T) {
	for _, name := range []string{"empty intermediate", "repeated page", "duplicate occurrence", "total mismatch", "revision changed", "final revision changed", "missing total", "missing offset", "missing truncated", "wrong truncation", "short final", "prefix mismatch", "malformed", "HTTP", "track HTTP", "track malformed", "track identity", "track artist", "missing bootstrap", "nonanonymous bootstrap", "invalid bootstrap", "oversize"} {
		t.Run(name, func(t *testing.T) {
			f := newAnonymousFixture(123, 100)
			if name == "missing bootstrap" {
				delete(f.session(), "accessToken")
			}
			if name == "nonanonymous bootstrap" {
				f.session()["isAnonymous"] = false
			}
			f.mutate = func(r *http.Request, d map[string]any) (int, string) {
				if strings.HasPrefix(r.URL.Path, "/embed/playlist/") && name == "invalid bootstrap" {
					return 200, `<script id="__NEXT_DATA__">{anonymous-secret-sentinel`
				}
				if strings.HasPrefix(r.URL.Path, "/embed/track/") {
					if name == "track HTTP" {
						return 500, "anonymous-secret-sentinel"
					}
					if name == "track malformed" {
						return 200, "anonymous-secret-sentinel"
					}
					e := d["props"].(map[string]any)["pageProps"].(map[string]any)["state"].(map[string]any)["data"].(map[string]any)["entity"].(map[string]any)
					if name == "track identity" {
						e["uri"] = "spotify:track:0000000000000000000001"
					}
					if name == "track artist" {
						e["artists"] = []any{}
					}
				}
				if r.URL.Host != "spclient.wg.spotify.com" {
					return 0, ""
				}
				c := d["contents"].(map[string]any)
				offset := c["pos"].(int)
				if name == "final revision changed" && f.calls[r.URL.Host+r.URL.Path+"?"+r.URL.RawQuery] > 1 {
					d["revision"] = "edited"
				}
				if offset == 50 {
					switch name {
					case "empty intermediate":
						c["items"] = []any{}
					case "repeated page":
						c["pos"] = 0
					case "duplicate occurrence":
						c["items"].([]any)[0].(map[string]any)["attributes"] = map[string]any{"itemId": "occurrence-0"}
					case "total mismatch":
						d["length"] = 124
					case "revision changed":
						d["revision"] = "edited"
					case "missing total":
						delete(d, "length")
					case "missing offset":
						delete(c, "pos")
					case "missing truncated":
						delete(c, "truncated")
					case "wrong truncation":
						c["truncated"] = false
					case "prefix mismatch":
						c["items"].([]any)[0].(map[string]any)["uri"] = f.uris[0]
					case "malformed":
						return 200, `{"accessToken":"anonymous-secret-sentinel",`
					case "HTTP":
						return 503, "anonymous-secret-sentinel"
					case "oversize":
						return 200, strings.Repeat("x", maxSpotifyPageBytes+1)
					}
				}
				if name == "short final" && offset == 100 {
					c["items"] = c["items"].([]any)[:22]
				}
				return 0, ""
			}
			var logs bytes.Buffer
			s := f.client(t)
			s.Log = slog.New(slog.NewTextHandler(&logs, nil))
			r := CollectionResolver{Spotify: s, Search: func(context.Context, string) (Track, error) {
				t.Error("incomplete playlist reached matching")
				return Track{}, nil
			}}
			got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/"+publicID)
			if err == nil || len(got.Tracks) != 0 {
				t.Fatal("partial collection escaped", len(got.Tracks))
			}
			if strings.Contains(err.Error(), "secret-sentinel") || strings.Contains(logs.String(), "secret-sentinel") {
				t.Fatal("token leaked")
			}
		})
	}
}
func TestAnonymousExpiryAndRefresh(t *testing.T) {
	for _, name := range []string{"expiry", "401", "401 twice", "expired refresh"} {
		t.Run(name, func(t *testing.T) {
			f := newAnonymousFixture(123, 100)
			body := embedHTML(f.envelope)
			if strings.Contains(name, "expir") {
				f.session()["accessTokenExpirationTimestampMs"] = time.Now().Add(-time.Hour).UnixMilli()
				body = embedHTML(f.envelope)
			}
			refreshes, requests := 0, 0
			f.mutate = func(r *http.Request, d map[string]any) (int, string) {
				if strings.HasPrefix(r.URL.Path, "/embed/playlist/") {
					refreshes++
					if name != "expired refresh" {
						f.session()["accessTokenExpirationTimestampMs"] = time.Now().Add(time.Hour).UnixMilli()
						f.session()["accessToken"] = "refreshed-secret-sentinel"
					}
				}
				if r.URL.Host == "spclient.wg.spotify.com" {
					requests++
					if name == "401 twice" || (name == "401" && requests == 1) {
						return 401, "secret-sentinel"
					}
				}
				return 0, ""
			}
			got, err := f.client(t).anonymousPlaylist(context.Background(), publicID, []byte(body), f.evidence)
			if name == "401 twice" || name == "expired refresh" {
				if err == nil || len(got.Tracks) != 0 {
					t.Fatal("accepted unusable token")
				}
			} else if err != nil || len(got.Tracks) != 123 {
				t.Fatal(len(got.Tracks), err)
			}
			if refreshes != 1 {
				t.Fatal("refresh not bounded", refreshes)
			}
		})
	}
}
func TestAnonymousCancellationAndTransportPrivacy(t *testing.T) {
	for _, name := range []string{"page", "hydrate", "transport"} {
		t.Run(name, func(t *testing.T) {
			f := newAnonymousFixture(123, 100)
			s := f.client(t)
			base := s.Client.Transport
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hit := (name != "hydrate" && r.URL.Host == "spclient.wg.spotify.com") || (name == "hydrate" && strings.HasPrefix(r.URL.Path, "/embed/track/"))
				if hit {
					if name == "transport" {
						return nil, errors.New("anonymous-secret-sentinel")
					}
					cancel()
					return nil, r.Context().Err()
				}
				return base.RoundTrip(r)
			})
			got, err := s.Collection(ctx, "playlist", publicID)
			if err == nil || len(got.Tracks) != 0 {
				t.Fatal("partial result")
			}
			if name != "transport" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("transport token leaked")
			}
		})
	}
}
func TestSpotifyPublicLargeLiveRegression(t *testing.T) {
	liveSpotifyRegression(t, "35m4wmHSlcS2RNsafX4dEg", 123)
}

func TestAnonymousHTTPFailuresNeverRetryOrFollowRedirects(t *testing.T) {
	for _, status := range []int{302, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newAnonymousFixture(123, 100)
			s := f.client(t)
			base := s.Client.Transport
			calls := 0
			s.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "untrusted.invalid" {
					t.Fatal("followed metadata redirect")
				}
				if r.URL.Host == "spclient.wg.spotify.com" {
					calls++
					resp := jsonResponse(status, "anonymous-secret-sentinel")
					resp.Header.Set("Location", "https://untrusted.invalid/")
					return resp, nil
				}
				return base.RoundTrip(r)
			})
			got, err := s.Collection(context.Background(), "playlist", publicID)
			if err == nil || len(got.Tracks) != 0 || calls != 1 {
				t.Fatal("unbounded retry or incomplete acceptance", calls)
			}
		})
	}
}
func TestAnonymousRefreshBetweenPages(t *testing.T) {
	f := newAnonymousFixture(123, 100)
	s := f.client(t)
	a := spotifyAnonymousSession{spotify: s, id: publicID}
	if err := a.bootstrap([]byte(embedHTML(f.envelope))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.page(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	a.expires = time.Now().Add(-time.Second)
	if _, err := a.page(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if !a.refreshed {
		t.Fatal("expiry ignored")
	}
	a.expires = time.Now().Add(-time.Second)
	if _, err := a.page(context.Background(), 100); err == nil {
		t.Fatal("second refresh allowed")
	}
	if f.calls["open.spotify.com/embed/playlist/"+publicID+"?"] != 1 {
		t.Fatal("refresh unbounded")
	}
}
func TestAnonymousBootstrapRefreshFailure(t *testing.T) {
	f := newAnonymousFixture(123, 100)
	f.session()["accessTokenExpirationTimestampMs"] = time.Now().Add(-time.Second).UnixMilli()
	body := embedHTML(f.envelope)
	f.mutate = func(r *http.Request, _ map[string]any) (int, string) {
		if strings.HasPrefix(r.URL.Path, "/embed/playlist/") {
			return 503, "anonymous-secret-sentinel"
		}
		return 0, ""
	}
	got, err := f.client(t).anonymousPlaylist(context.Background(), publicID, []byte(body), f.evidence)
	if err == nil || len(got.Tracks) != 0 || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("refresh failed unsafely")
	}
}
