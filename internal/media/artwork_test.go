package media

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestArtworkPriorityAndInvalidURLs(t *testing.T) {
	spotify := "https://i.scdn.co/image/cover"
	youtube := "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"
	for _, tc := range []struct {
		track Track
		want  string
	}{
		{Track{SpotifyArtworkURL: spotify, YouTubeThumbnailURL: youtube, VideoID: "abcdefghijk"}, spotify},
		{Track{YouTubeThumbnailURL: youtube}, youtube},
		{Track{VideoID: "abcdefghijk"}, youtube},
		{Track{}, ""},
	} {
		if got := ArtworkURL(tc.track); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	for _, bad := range []string{"not a URL", "http://example.com/image", "javascript:alert(1)", "data:image/png;base64,x", "file:///etc/passwd", "https://user:password@example.com/image", "https://example.com:8080/image", "https://example.com/%zz", "https://example.com/\nimage"} {
		if SafeArtworkURL(bad) != "" {
			t.Fatal("invalid artwork accepted", bad)
		}
		if got := ArtworkURL(Track{SpotifyArtworkURL: bad, YouTubeThumbnailURL: youtube}); got != youtube {
			t.Fatal("invalid Spotify artwork masked fallback", got)
		}
	}
}
func TestSpotifyArtworkMetadataAndMatching(t *testing.T) {
	for _, kind := range []string{"track", "album", "playlist"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			s := &Spotify{ClientID: "fixture", ClientSecret: "fixture", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host == "accounts.spotify.com" {
					return jsonResponse(200, `{"access_token":"fixture","expires_in":3600}`), nil
				}
				track := `{"id":"1234567890123456789012","name":"Paranoid","type":"track","artists":[{"name":"Black Sabbath"}],"album":{"name":"Paranoid","images":[{"url":"https://i.scdn.co/image/track-album"}]}}`
				if strings.HasSuffix(r.URL.Path, "/tracks") {
					return jsonResponse(200, fmt.Sprintf(`{"total":1,"items":[%s],"next":null}`, track)), nil
				}
				if strings.HasSuffix(r.URL.Path, "/items") {
					return jsonResponse(200, fmt.Sprintf(`{"total":1,"items":[{"track":%s}],"next":null}`, track)), nil
				}
				if kind == "track" {
					return jsonResponse(200, track), nil
				}
				return jsonResponse(200, `{"name":"Paranoid","images":[{"url":"https://i.scdn.co/image/album"}]}`), nil
			})}}
			c, err := s.Collection(context.Background(), kind, "1234567890123456789012")
			if err != nil || len(c.Tracks) != 1 {
				t.Fatal(c, err)
			}
			want := "https://i.scdn.co/image/track-album"
			if kind == "album" {
				want = "https://i.scdn.co/image/album"
			}
			original := c.Tracks[0]
			if original.SpotifyArtworkURL != want {
				t.Fatal(original)
			}
			before := calls
			r := CollectionResolver{Search: func(context.Context, string) (Track, error) {
				return Track{URL: "https://youtube.com/watch?v=abcdefghijk", VideoID: "abcdefghijk", YouTubeThumbnailURL: "https://i.ytimg.com/vi/abcdefghijk/default.jpg"}, nil
			}}
			matched, err := r.MatchSpotify(context.Background(), original, false)
			if err != nil || matched.SpotifyID != original.SpotifyID || matched.SpotifyArtworkURL != want || ArtworkURL(matched) != want || matched.YouTubeThumbnailURL == "" || calls != before {
				t.Fatal(matched, err)
			}
		})
	}
}
func TestYouTubeArtworkCapturedDuringResolution(t *testing.T) {
	r, _ := resolverFixture(t, `{"id":"abcdefghijk","title":"Exact video","thumbnail":"https://i.ytimg.com/vi/abcdefghijk/custom.jpg"}`)
	track, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk")
	if err != nil || track.YouTubeThumbnailURL != "https://i.ytimg.com/vi/abcdefghijk/custom.jpg" {
		t.Fatal(track, err)
	}
	r, _ = resolverFixture(t, `{"entries":[{"id":"abcdefghijk","title":"Track","thumbnail":"https://i.ytimg.com/vi/abcdefghijk/custom.jpg"}]}`)
	collection, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://youtube.com/playlist?list=PLtest")
	if err != nil || collection.Tracks[0].YouTubeThumbnailURL != track.YouTubeThumbnailURL {
		t.Fatal(collection, err)
	}
}
func TestSpotifyTrackEmbedArtwork(t *testing.T) {
	body := []byte(`<script id="__NEXT_DATA__">{"props":{"pageProps":{"state":{"data":{"entity":{"type":"track","uri":"spotify:track:1234567890123456789012","title":"Paranoid","artists":[{"name":"Black Sabbath"}],"coverArt":{"sources":[{"url":"https://i.scdn.co/image/cover"}]}}}}}}}</script>`)
	tr, err := parseSpotifyTrackEmbed(body, "spotify:track:1234567890123456789012")
	if err != nil || tr.SpotifyArtworkURL != "https://i.scdn.co/image/cover" {
		t.Fatal(tr, err)
	}
}

func TestArtworkDoesNotChangeRecordingMappingKey(t *testing.T) {
	track := Track{Title: "Paranoid", SpotifyID: "original", Artist: "Black Sabbath"}
	before := MappingKey(track, nil)
	track.SpotifyArtworkURL = "https://i.scdn.co/image/cover"
	track.YouTubeThumbnailURL = "https://i.ytimg.com/vi/abcdefghijk/default.jpg"
	if after := MappingKey(track, nil); after != before {
		t.Fatal("artwork changed identity/cache key", before, after)
	}
}

func TestSpotifyPlaylistEmbedIndividualArtwork(t *testing.T) {
	d, e := embedFixture(t)
	tracks := e["trackList"].([]any)
	for i, raw := range tracks {
		raw.(map[string]any)["imageUri"] = fmt.Sprintf("https://i.scdn.co/image/cover-%d", i)
	}
	c, err := parseSpotifyEmbed([]byte(embedHTML(d)), "37i9dQZF1DZ06evO3VkoW4", embedEvidence(e))
	if err != nil {
		t.Fatal(err)
	}
	for i, tr := range c.Tracks {
		if tr.SpotifyArtworkURL != fmt.Sprintf("https://i.scdn.co/image/cover-%d", i) {
			t.Fatal("lost individual cover", i, tr)
		}
	}
}

type artworkCacheFixture struct {
	brokenMappings
	cached   Track
	hit      bool
	err      error
	loads    int
	deadline time.Time
}

func (s *artworkCacheFixture) LoadCatalog(ctx context.Context, _ string) (Track, bool, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return Track{}, false, fmt.Errorf("missing deadline")
	}
	if !s.deadline.IsZero() && !s.deadline.Equal(deadline) {
		return Track{}, false, fmt.Errorf("lookup budget reset per track")
	}
	s.deadline = deadline
	s.loads++
	return s.cached, s.hit, s.err
}
func (*artworkCacheFixture) SaveCatalog(context.Context, Track) error { return nil }
func TestCatalogArtworkRecoveryValidatesIdentityAndPreservesMetadata(t *testing.T) {
	for _, mode := range []string{"hit", "miss", "error", "wrong ID", "wrong source", "invalid URL", "existing cover", "direct youtube"} {
		t.Run(mode, func(t *testing.T) {
			source := Track{SpotifyID: "same-id", RequestedSource: "spotify", Title: "Song", Artist: "Artist", VideoID: "abcdefghijk", YouTubeThumbnailURL: "https://i.ytimg.com/selected", RequestedBy: "traveler"}
			store := &artworkCacheFixture{cached: Track{SpotifyID: source.SpotifyID, RequestedSource: "spotify", SpotifyArtworkURL: "https://i.scdn.co/image/cached"}, hit: true}
			expected := source
			switch mode {
			case "hit":
				expected.SpotifyArtworkURL = store.cached.SpotifyArtworkURL
			case "miss":
				store.hit = false
			case "error":
				store.err = fmt.Errorf("SQLite unavailable")
			case "wrong ID":
				store.cached.SpotifyID = "other-id"
			case "wrong source":
				store.cached.RequestedSource = "youtube"
			case "invalid URL":
				store.cached.SpotifyArtworkURL = "file:///bad"
			case "existing cover":
				source.SpotifyArtworkURL = "https://i.scdn.co/image/original"
				expected = source
			case "direct youtube":
				source.RequestedSource = "youtube"
				source.SpotifyID = ""
				expected = source
			}
			tracks := []Track{source, source}
			resolver := CollectionResolver{YouTube: Resolver{Mappings: store}}
			resolver.restoreCatalogArtwork(context.Background(), tracks)
			for _, got := range tracks {
				if !reflect.DeepEqual(got, expected) {
					t.Fatal(mode, got, expected)
				}
			}
			if (mode == "existing cover" || mode == "direct youtube") && store.loads != 0 {
				t.Fatal("unnecessary catalog read")
			}
			if !store.deadline.IsZero() && time.Until(store.deadline) > 200*time.Millisecond {
				t.Fatal("unbounded artwork lookup")
			}
		})
	}
}
