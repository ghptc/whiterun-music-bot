package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const spotifyTrackJSON = `{"id":"1234567890123456789012","name":"Song","type":"track","artists":[{"name":"Artist"}]}`

func TestSpotifyPaginationAndTokenReuse(t *testing.T) {
	for _, kind := range []string{"playlist", "album"} {
		t.Run(kind, func(t *testing.T) {
			tokens, pages := 0, 0
			s := &Spotify{ClientID: "id", ClientSecret: "secret", RefreshToken: "refresh", Market: "BR"}
			s.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "accounts.spotify.com" {
					tokens++
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					user, pass, _ := r.BasicAuth()
					if user != "id" || pass != "secret" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh" {
						t.Fatal("wrong auth")
					}
					return jsonResponse(200, `{"access_token":"access","expires_in":3600}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer access" {
					t.Fatal("missing bearer")
				}
				endpoint := "tracks"
				if kind == "playlist" {
					endpoint = "items"
				}
				if r.URL.Query().Get("market") != "BR" {
					t.Fatal("missing market on " + r.URL.Path)
				}
				if !strings.HasSuffix(r.URL.Path, "/"+endpoint) {
					return jsonResponse(200, `{"name":"Collection"}`), nil
				}
				pages++
				item := spotifyTrackJSON
				if kind == "playlist" {
					item = `{"item":` + item + `}`
				}
				if pages == 1 {
					return jsonResponse(200, `{"total":3,"items":[`+item+`,null],"next":"https://evil.test/credentials"}`), nil
				}
				if r.URL.Query().Get("offset") != "2" || r.URL.Host != "api.spotify.com" {
					t.Fatal("bad pagination")
				}
				return jsonResponse(200, `{"total":3,"items":[`+item+`],"next":null}`), nil
			})}
			got, err := s.Collection(context.Background(), kind, "1234567890123456789012")
			if err != nil || tokens != 1 || pages != 2 || len(got.Tracks) != 2 || got.Skipped != 1 || got.Title != "Collection" || got.Tracks[0].Artist != "Artist" {
				t.Fatalf("%+v %v tokens=%d pages=%d", got, err, tokens, pages)
			}
		})
	}
}
func TestSpotifyFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "accounts.spotify.com" {
					return jsonResponse(200, `{"access_token":"a","expires_in":3600}`), nil
				}
				return jsonResponse(status, `secret internal error`), nil
			})}}
			_, err := s.Collection(context.Background(), "playlist", "1234567890123456789012")
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("%v", err)
			}
			if status == 401 && !errors.Is(err, ErrSpotifyAuth) {
				t.Fatal(err)
			}
			if (status == 403 || status == 404) && !errors.Is(err, ErrSpotifyAccess) {
				t.Fatal(err)
			}
		})
	}
	if _, err := (&Spotify{}).accessToken(context.Background()); !errors.Is(err, ErrSpotifyConfig) {
		t.Fatal(err)
	}
}

func TestSpotifyUnavailableEntriesAndBounds(t *testing.T) {
	for _, tt := range []struct {
		name, body     string
		count, skipped int
		want           error
	}{
		{"unavailable", `{"items":[{"item":null},{"item":` + spotifyTrackJSON + `,"is_local":true},{"item":{"type":"episode"}},{"track":` + spotifyTrackJSON + `}],"next":null}`, 1, 3, nil},
		{"too large", `{"total":501,"items":[]}`, 0, 0, ErrCollectionTooLarge},
		{"empty", `{"items":[],"next":null}`, 0, 0, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "accounts.spotify.com" {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("grant_type") != "client_credentials" {
						t.Fatal("wrong grant")
					}
					return jsonResponse(200, `{"access_token":"access","expires_in":3600}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/items") {
					return jsonResponse(200, tt.body), nil
				}
				return jsonResponse(200, `{"name":"Playlist"}`), nil
			})}}
			got, err := s.Collection(context.Background(), "playlist", "1234567890123456789012")
			if !errors.Is(err, tt.want) || len(got.Tracks) != tt.count || got.Skipped != tt.skipped {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestSpotifySingleTrack(t *testing.T) {
	s := &Spotify{ClientID: "id", ClientSecret: "secret", Market: "BR", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "accounts.spotify.com" {
			return jsonResponse(200, `{"access_token":"a","expires_in":3600}`), nil
		}
		if r.URL.Path != "/v1/tracks/1234567890123456789012" || r.URL.Query().Get("market") != "BR" {
			t.Fatal(r.URL)
		}
		return jsonResponse(200, `{"id":"1234567890123456789012","name":"505","type":"track","duration_ms":253000,"album":{"name":"Favourite Worst Nightmare"},"artists":[{"name":"Arctic Monkeys"}]}`), nil
	})}}
	got, err := s.Collection(context.Background(), "track", "1234567890123456789012")
	if err != nil || got.IsCollection || len(got.Tracks) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	track := got.Tracks[0]
	if track.Artist != "Arctic Monkeys" || track.Duration.Seconds() != 253 || track.Album != "Favourite Worst Nightmare" || track.SourceURL != "https://open.spotify.com/track/1234567890123456789012" {
		t.Fatal(track)
	}
}
func TestSpotifyTokenAuthorizationError(t *testing.T) {
	s := &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(400, "private details"), nil })}}
	_, err := s.Collection(context.Background(), "track", "1234567890123456789012")
	if !errors.Is(err, ErrSpotifyAuth) || strings.Contains(err.Error(), "private details") {
		t.Fatal(err)
	}
}

func TestSpotifyLaterPageFailureIsAtomic(t *testing.T) {
	pages := 0
	s := &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "accounts.spotify.com" {
			return jsonResponse(200, `{"access_token":"a","expires_in":3600}`), nil
		}
		if r.URL.Host == "open.spotify.com" {
			return jsonResponse(403, ""), nil
		}
		if !strings.HasSuffix(r.URL.Path, "/items") {
			return jsonResponse(200, `{"name":"Playlist"}`), nil
		}
		pages++
		if pages == 1 {
			return jsonResponse(200, `{"items":[{"item":`+spotifyTrackJSON+`}],"next":"next"}`), nil
		}
		return jsonResponse(403, `unavailable`), nil
	})}}
	r := CollectionResolver{Spotify: s, Search: func(context.Context, string) (Track, error) {
		t.Fatal("matched incomplete collection")
		return Track{}, nil
	}}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/1234567890123456789012")
	if !errors.Is(err, ErrSpotifyAccess) || len(got.Tracks) != 0 || pages != 2 {
		t.Fatal(got, err, pages)
	}
}

func TestSpotifyIdentityPositionsAndTruncatedPagination(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		s := &Spotify{ClientID: "id", ClientSecret: "secret", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "accounts.spotify.com" {
				return jsonResponse(200, `{"access_token":"access","expires_in":3600}`), nil
			}
			if !strings.HasSuffix(r.URL.Path, "/items") {
				return jsonResponse(200, `{"name":"collection"}`), nil
			}
			item := `{"item":{"id":"1234567890123456789012","name":"Song","type":"track","duration_ms":210000,"explicit":true,"external_ids":{"isrc":"US1234567890"},"artists":[{"name":"Artist"}]}}`
			total := 3
			if truncated {
				total = 4
			}
			return jsonResponse(200, fmt.Sprintf(`{"total":%d,"items":[%s,null,%s],"next":null}`, total, item, item)), nil
		})}}
		got, err := s.Collection(context.Background(), "playlist", "1234567890123456789012")
		if truncated {
			if !errors.Is(err, ErrSpotifyIncomplete) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || len(got.Tracks) != 2 || got.Skipped != 1 {
			t.Fatal(got, err)
		}
		for i, track := range got.Tracks {
			if track.Position != i*2+1 || track.Total != 3 || track.SpotifyID != "1234567890123456789012" || track.ISRC != "US1234567890" || track.Explicit == nil || !*track.Explicit {
				t.Fatal(track)
			}
		}
	}
}
