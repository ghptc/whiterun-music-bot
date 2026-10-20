package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPlaybackManifestDefers500TailMetadata(t *testing.T) {
	f := newAnonymousFixture(500, 100)
	// A provider failure in any tail embed must not prevent the first track.
	f.mutate = func(r *http.Request, _ map[string]any) (int, string) {
		if strings.HasPrefix(r.URL.Path, "/embed/track/") {
			return 504, ""
		}
		return 0, ""
	}
	r := CollectionResolver{Spotify: f.client(t), Search: func(context.Context, string) (Track, error) {
		return Track{URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
	}}
	c, progressive, err := r.Prepare(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err != nil || !progressive || len(c.Tracks) != 500 {
		t.Fatalf("tracks=%d progressive=%v error=%v", len(c.Tracks), progressive, err)
	}
	for i, tr := range c.Tracks {
		if tr.SpotifyID != fmt.Sprintf("%022d", i+1) || tr.Position != i+1 || tr.Total != 500 || tr.MetadataPending != (i >= 100) {
			t.Fatalf("occurrence %d: %+v", i, tr)
		}
	}
	if _, err = r.MatchSpotify(context.Background(), c.Tracks[0], true); err != nil {
		t.Fatal(err)
	}
	for key := range f.calls {
		if strings.Contains(key, "/embed/track/") {
			t.Fatal("tail fetched before first match", key)
		}
	}
	if _, err = r.MatchSpotify(context.Background(), c.Tracks[100], false); err == nil {
		t.Fatal("tail error hidden")
	}
	// Persistence carries the exact identity and pending state, no session/token.
	raw, _ := json.Marshal(c.Tracks[101])
	var restored Track
	if err = json.Unmarshal(raw, &restored); err != nil || !restored.MetadataPending || restored.SpotifyID != c.Tracks[101].SpotifyID {
		t.Fatal("pending occurrence not durable", err)
	}
}

func TestPlaybackTailEnrichmentPreservesIdentityAndOccurrence(t *testing.T) {
	f := newAnonymousFixture(123, 100)
	r := CollectionResolver{Spotify: f.client(t), Search: func(_ context.Context, q string) (Track, error) {
		if strings.Contains(q, "Loading") {
			t.Error("placeholder searched")
		}
		return Track{URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
	}}
	c, _, err := r.Prepare(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err != nil {
		t.Fatal(err)
	}
	original := c.Tracks[110]
	original.RequestedBy = "listener"
	got, err := r.MatchSpotify(context.Background(), original, false)
	if err != nil || got.MetadataPending || got.SpotifyID != original.SpotifyID || got.Title != "Track 111" || got.Position != 111 || got.Total != 123 || got.RequestedBy != "listener" {
		t.Fatalf("%+v %v", got, err)
	}
	f.mutate = func(req *http.Request, data map[string]any) (int, string) {
		if strings.HasPrefix(req.URL.Path, "/embed/track/") {
			return 200, `<script id="__NEXT_DATA__">{"props":{"pageProps":{"state":{"data":{"entity":{"type":"track","uri":"spotify:track:0000000000000000000001","title":"Wrong","duration":120000,"artists":[{"name":"Artist"}]}}}}}}</script>`
		}
		return 0, ""
	}
	if _, err = r.MatchSpotify(context.Background(), original, false); err == nil {
		t.Fatal("different Spotify identity accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.MatchSpotify(ctx, original, false); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestPlaybackManifestRejectsLaterPageFailure(t *testing.T) {
	f := newAnonymousFixture(500, 100)
	f.mutate = func(req *http.Request, _ map[string]any) (int, string) {
		if req.URL.Host == "spclient.wg.spotify.com" && req.URL.Query().Get("from") == "150" {
			return 504, ""
		}
		return 0, ""
	}
	_, _, err := (CollectionResolver{Spotify: f.client(t)}).Prepare(context.Background(), "https://open.spotify.com/playlist/"+publicID)
	if err == nil {
		t.Fatal("partial occurrence manifest accepted")
	}
}

func TestPlaybackDeferredMetadataUsesExistingCatalog(t *testing.T) {
	id := fmt.Sprintf("%022d", 111)
	cached := Track{SpotifyID: id, SourceURL: "https://open.spotify.com/track/" + id, RequestedSource: "spotify", Title: "Cached identity", Artist: "Artist", SpotifyArtworkURL: "https://i.scdn.co/image/cached"}
	store := &artworkCacheFixture{cached: cached, hit: true}
	r := CollectionResolver{YouTube: Resolver{Mappings: store}, Search: func(_ context.Context, q string) (Track, error) {
		if !strings.Contains(q, "Cached identity") {
			t.Error("cached identity not used", q)
		}
		return Track{URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
	}}
	pending := Track{SpotifyID: id, SourceURL: cached.SourceURL, RequestedSource: "spotify", MetadataPending: true, Position: 111, Total: 500, RequestedBy: "listener"}
	got, err := r.MatchSpotify(context.Background(), pending, false)
	if err != nil || got.MetadataPending || got.Title != cached.Title || got.SpotifyArtworkURL != cached.SpotifyArtworkURL || got.Position != 111 || got.Total != 500 || got.RequestedBy != "listener" || store.loads != 1 {
		t.Fatal("catalog fast path failed", got, err, store.loads)
	}
	// Restore the entire cached metadata before queue reservation, preserving positions.
	store.deadline = time.Time{}
	tracks := []Track{pending}
	r.restoreCatalogArtwork(context.Background(), tracks)
	if tracks[0].MetadataPending || tracks[0].Title != cached.Title || tracks[0].Position != 111 || tracks[0].RequestedBy != "listener" {
		t.Fatal("catalog propagation failed", tracks[0])
	}
}
