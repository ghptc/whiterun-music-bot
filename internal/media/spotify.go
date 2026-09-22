package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrSpotifyConfig = errors.New("Spotify is not configured. Ask the bot operator to configure Spotify credentials.")
var ErrSpotifyAccess = errors.New("Spotify denied access to this collection. Check the authorized account and playlist permissions.")

type Spotify struct {
	ClientID, ClientSecret, RefreshToken string
	Market                               string
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
		return "", fmt.Errorf("Spotify token request status %d", resp.StatusCode)
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
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf(
			"%w: status=%d path=%s",
			ErrSpotifyAccess,
			resp.StatusCode,
			path,
		)
	}
	if resp.StatusCode != http.StatusOK {
    	return fmt.Errorf("Spotify API status %d path=%s", resp.StatusCode, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

type spotifyTrack struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Local    bool   `json:"is_local"`
	Playable *bool  `json:"is_playable"`
	Artists  []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

func (s *Spotify) Collection(ctx context.Context, kind, id string) (Collection, error) {
	if (kind != "playlist" && kind != "album") || !spotifyID.MatchString(id) {
		return Collection{}, errors.New("invalid Spotify collection")
	}
	root := kind + "s/" + id
	var meta struct {
		Name string `json:"name"`
	}
	metadataPath := root
	if s.Market != "" {
		metadataPath += "?" + url.Values{"market": {s.Market}}.Encode()
	}
	if err := s.get(ctx, metadataPath, &meta); err != nil {
		return Collection{}, err
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
		for _, raw := range page.Items {
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
			if track == nil || track.Local || track.Type != "track" || !spotifyID.MatchString(track.ID) || track.Name == "" || len(track.Artists) == 0 || track.Artists[0].Name == "" || (track.Playable != nil && !*track.Playable) {
				result.Skipped++
				continue
			}
			result.Tracks = append(result.Tracks, Track{Title: track.Name, Artist: track.Artists[0].Name, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/" + track.ID})
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
