package media

import (
	"net/url"
	"strings"
)

// SafeArtworkURL validates display metadata only; it never fetches the URL.
// HTTPS prevents active/data/file schemes in both Discord and exported galleries.
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
