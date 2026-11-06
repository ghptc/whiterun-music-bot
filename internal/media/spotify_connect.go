package media

import "errors"

func nativeSpotifyTrack(t Track) (Track, error) {
	if (t.SpotifyOriginalID != "" && t.SpotifyOriginalID != t.SpotifyID) || !spotifyID.MatchString(t.SpotifyID) || t.Duration <= 0 || t.MetadataPending {
		return Track{}, errors.New("exact Spotify identity or duration unavailable")
	}
	t.RequestedSource = "spotify_connect"
	t.URL = "spotify:track:" + t.SpotifyID
	t.VideoID = ""
	t.YouTubeThumbnailURL = ""
	t.VerifyBeforePlay = false
	t.MatchStatus = MatchConfirmed
	t.MatchReason = "exact_spotify_uri"
	return t, nil
}
