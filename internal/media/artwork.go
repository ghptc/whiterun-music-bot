package media

import (
	"context"
	"net/url"
	"strings"
)

// SafeArtworkURL validates display metadata only; it never fetches the URL.
// HTTPS prevents active/data/file schemes in Discord media metadata.
func SafeArtworkURL(raw string) string {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\t") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" {
		return ""
	}
	return u.String()
}

// ArtworkURL preserves Spotify album art even before audio resolution completes.
func ArtworkURL(t Track) string {
	if art := SafeArtworkURL(t.SpotifyArtworkURL); art != "" {
		return art
	}
	if art := SafeArtworkURL(t.YouTubeThumbnailURL); art != "" {
		return art
	}
	if videoID.MatchString(t.VideoID) {
		return "https://i.ytimg.com/vi/" + t.VideoID + "/hqdefault.jpg"
	}
	return ""
}

type spotifyImage struct {
	URL string `json:"url"`
}

func spotifyArtwork(images []spotifyImage) string {
	for _, img := range images {
		if art := SafeArtworkURL(img.URL); art != "" {
			return art
		}
	}
	return ""
}

// Reuse album art only by the exact Spotify identity, before entries enter the
// queue. A single bounded catalog context covers the entire collection; a busy
// SQLite database must not add a per-track delay or block audio startup.
func (r CollectionResolver) restoreCatalogArtwork(ctx context.Context, tracks []Track) {
	store, ok := r.YouTube.Mappings.(CatalogStore)
	if !ok {
		return
	}
	bounded, cancel := catalogContext(ctx)
	defer cancel()
	for i := range tracks {
		t := &tracks[i]
		if t.RequestedSource != "spotify" || t.SpotifyID == "" || SafeArtworkURL(t.SpotifyArtworkURL) != "" {
			continue
		}
		if bounded.Err() != nil {
			return
		}
		cached, hit, err := store.LoadCatalog(bounded, t.SpotifyID)
		if err != nil {
			if r.Log != nil {
				r.Log.Warn("catalog_artwork_read_failed", "error", err)
			}
			return
		}
		if hit && cached.SpotifyID == t.SpotifyID && cached.RequestedSource == "spotify" {
			if t.MetadataPending && completeSpotifyMetadata(cached, t.SpotifyID) {
				position, total, requester := t.Position, t.Total, t.RequestedBy
				*t = cached
				t.Position, t.Total, t.RequestedBy = position, total, requester
			}
			t.SpotifyArtworkURL = SafeArtworkURL(cached.SpotifyArtworkURL)
		}
	}
}
