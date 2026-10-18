package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrSpotifyConfig = errors.New("Spotify is not configured. Ask the bot operator to configure Spotify credentials.")
var ErrSpotifyAuth = errors.New("Spotify authorization failed. Ask the operator to check credentials and refresh-token authorization.")
var ErrSpotifyAccess = errors.New("Spotify could not access this track or collection. Check the authorized account, app access mode, and playlist permissions.")

type Spotify struct {
	ClientID, ClientSecret, RefreshToken string
	Market                               string
	Log                                  *slog.Logger
	Client                               *http.Client
	mu                                   sync.Mutex
	token                                string
	expires                              time.Time
}

func (s *Spotify) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Spotify) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ClientID == "" || s.ClientSecret == "" {
		return "", ErrSpotifyConfig
	}
	if s.token != "" && time.Now().Before(s.expires) {
		return s.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if s.RefreshToken != "" {
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", s.RefreshToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(s.ClientID, s.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status=%d", ErrSpotifyAuth, resp.StatusCode)
	}
	var token struct {
		Access  string `json:"access_token"`
		Expires int    `json:"expires_in"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return "", err
	}
	if token.Access == "" || token.Expires <= 0 {
		return "", errors.New("Spotify returned invalid token")
	}
	s.token = token.Access
	s.expires = time.Now().Add(time.Duration(token.Expires)*time.Second - 30*time.Second)
	if token.Refresh != "" {
		s.RefreshToken = token.Refresh
	}
	return s.token, nil
}
func (s *Spotify) get(ctx context.Context, path string, out any) error {
	token, err := s.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.spotify.com/v1/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return fmt.Errorf("%w: status=401 path=%s", ErrSpotifyAuth, path)
	}
	if resp.StatusCode == 403 || resp.StatusCode == 404 {
		return &spotifyAccessError{Status: resp.StatusCode, Path: path}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Spotify API status %d path=%s", resp.StatusCode, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

type spotifyTrack struct {
	Explicit    *bool `json:"explicit"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
	DurationMS int `json:"duration_ms"`
	Album      struct {
		Name string `json:"name"`
	} `json:"album"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Local    bool   `json:"is_local"`
	Playable *bool  `json:"is_playable"`
	Artists  []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

type spotifyAccessError struct {
	Status int
	Path   string
}

func (e *spotifyAccessError) Error() string {
	return fmt.Sprintf("%s: status=%d path=%s", ErrSpotifyAccess, e.Status, e.Path)
}
func (e *spotifyAccessError) Unwrap() error { return ErrSpotifyAccess }

func (s *Spotify) Collection(ctx context.Context, kind, id string) (Collection, error) {
	result, err := s.apiCollection(ctx, kind, id)
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if err == nil {
		log.InfoContext(ctx, "Spotify metadata resolved", "stage", "spotify_metadata", "source", "api", "tracks", len(result.Tracks))
		return result, nil
	}
	var access *spotifyAccessError
	if kind != "playlist" || !errors.As(err, &access) || ctx.Err() != nil {
		return Collection{}, err
	}
	log.InfoContext(ctx, "Spotify public page fallback", "stage", "spotify_metadata", "source", "public_webpage", "api_status", access.Status)
	result, fallbackErr := s.publicPlaylist(ctx, id)
	if fallbackErr != nil {
		return Collection{}, fmt.Errorf("Spotify API failed: %w; public webpage fallback failed: %w", err, fallbackErr)
	}
	log.InfoContext(ctx, "Spotify metadata resolved", "stage", "spotify_metadata", "source", "public_webpage", "tracks", len(result.Tracks))
	return result, nil
}

func (s *Spotify) apiCollection(ctx context.Context, kind, id string) (Collection, error) {
	if (kind != "track" && kind != "playlist" && kind != "album") || !spotifyID.MatchString(id) {
		return Collection{}, ErrSpotifyURL
	}
	root := kind + "s/" + id
	var meta spotifyTrack
	metadataPath := root
	if s.Market != "" {
		metadataPath += "?" + url.Values{"market": {s.Market}}.Encode()
	}
	if err := s.get(ctx, metadataPath, &meta); err != nil {
		return Collection{}, err
	}
	if kind == "track" {
		track, ok := spotifyMetadata(&meta)
		if !ok {
			return Collection{}, ErrNoTracks
		}
		return Collection{Title: track.Title, SourceURL: track.SourceURL, Tracks: []Track{track}}, nil
	}
	result := Collection{Title: meta.Name, SourceURL: "https://open.spotify.com/" + kind + "/" + id, IsCollection: true}
	endpoint := "tracks"
	if kind == "playlist" {
		endpoint = "items"
	}
	// Construct pagination URLs ourselves; never forward credentials to a next URL.
	for offset := 0; ; {
		params := url.Values{"limit": {"50"}, "offset": {fmt.Sprint(offset)}}
		if s.Market != "" {
			params.Set("market", s.Market)
		}
		var page struct {
			Items []json.RawMessage `json:"items"`
			Next  *string           `json:"next"`
			Total int               `json:"total"`
		}
		if err := s.get(ctx, root+"/"+endpoint+"?"+params.Encode(), &page); err != nil {
			return Collection{}, err
		}
		if page.Total > MaxCollectionTracks || offset+len(page.Items) > MaxCollectionTracks {
			return Collection{}, ErrCollectionTooLarge
		}
		for pageIndex, raw := range page.Items {
			var track *spotifyTrack
			if kind == "playlist" {
				var entry struct {
					Item  *spotifyTrack `json:"item"`
					Track *spotifyTrack `json:"track"`
					Local bool          `json:"is_local"`
				}
				if err := json.Unmarshal(raw, &entry); err != nil {
					return Collection{}, err
				}
				track = entry.Item
				if track == nil {
					track = entry.Track
				} // older extended-quota response schema
				if entry.Local {
					track = nil
				}
			} else if err := json.Unmarshal(raw, &track); err != nil {
				return Collection{}, err
			}
			metadata, ok := spotifyMetadata(track)
			if !ok {
				result.Skipped++
				continue
			}
			metadata.Position, metadata.Total = offset+pageIndex+1, page.Total
			if kind == "album" {
				metadata.Album = meta.Name
			}
			result.Tracks = append(result.Tracks, metadata)
		}
		offset += len(page.Items)
		if page.Next == nil || *page.Next == "" {
			break
		}
		if len(page.Items) == 0 {
			return Collection{}, errors.New("Spotify pagination made no progress")
		}
		if offset >= MaxCollectionTracks {
			return Collection{}, ErrCollectionTooLarge
		}
	}
	return result, nil
}

func spotifyMetadata(track *spotifyTrack) (Track, bool) {
	if track == nil || track.Local || track.Type != "track" || !spotifyID.MatchString(track.ID) || track.Name == "" || len(track.Artists) == 0 || track.Artists[0].Name == "" || (track.Playable != nil && !*track.Playable) {
		return Track{}, false
	}
	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if artist.Name != "" {
			artists = append(artists, artist.Name)
		}
	}
	return Track{SpotifyID: track.ID, ISRC: track.ExternalIDs.ISRC, Explicit: track.Explicit, Artists: artists, Title: track.Name, Artist: track.Artists[0].Name, Album: track.Album.Name, Duration: time.Duration(track.DurationMS) * time.Millisecond, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/" + track.ID}, true
}
