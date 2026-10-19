package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const MaxCollectionTracks = 500

var ErrCollectionTooLarge = errors.New("Collections are limited to 500 tracks.")
var ErrSpotifyURL = errors.New("Invalid Spotify link. Use an open.spotify.com track, album, or playlist URL.")
var ErrSpotifyMatch = errors.New("Could not find a matching YouTube recording for this Spotify track.")
var ErrNoTracks = errors.New("No playable tracks were found.")

type Collection struct {
	Title        string
	SourceURL    string
	Tracks       []Track
	Skipped      int
	IsCollection bool
}
type SpotifyProvider interface {
	Collection(context.Context, string, string) (Collection, error)
}
type CollectionResolver struct {
	YouTube Resolver
	Spotify SpotifyProvider
	Log     *slog.Logger
	// Search uses the existing music ranking by default; injectable for offline tests.
	Search  func(context.Context, string) (Track, error)
	Limiter *MatchLimiter
}

var collectionID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var spotifyID = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

// CollectionURL canonicalizes only supported provider URLs before external calls.
func CollectionURL(query string) (source, kind, id string, err error) {
	s := strings.TrimSpace(query)
	if kind, id, recognized, parseErr := spotifyURL(s); recognized {
		if parseErr != nil {
			return "", "", "", parseErr
		}
		return "spotify", kind, id, nil
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "youtube.com/") || strings.HasPrefix(lower, "www.youtube.com/") || strings.HasPrefix(lower, "music.youtube.com/") || strings.HasPrefix(lower, "m.youtube.com/") || strings.HasPrefix(lower, "youtu.be/") {
		s = "https://" + s
	}
	if !strings.Contains(s, "://") {
		return
	}
	u, e := url.Parse(s)
	if e != nil {
		return "", "", "", e
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return
	}
	if u.User != nil || u.Port() != "" {
		return "", "", "", errors.New("invalid media URL")
	}
	switch strings.ToLower(u.Hostname()) {
	case "youtube.com", "www.youtube.com", "music.youtube.com", "m.youtube.com", "youtu.be":
		if list := u.Query().Get("list"); list != "" && u.Path == "/playlist" {
			if !collectionID.MatchString(list) {
				return "", "", "", errors.New("invalid playlist ID")
			}
			return "youtube", "playlist", list, nil
		}
	}
	return
}

// Recognize the authority separately from validating the resource path. Even
// malformed paths, schemes, and bare-host links must never become search text.
func spotifyURL(s string) (kind, id string, recognized bool, err error) {
	if strings.HasPrefix(strings.ToLower(s), "spotify:") || strings.HasPrefix(strings.ToLower(s), "open.spotify.com:") {
		return "", "", true, ErrSpotifyURL
	}
	u, parseErr := url.Parse(s)
	if parseErr != nil {
		// Parse the authority alone when an invalid path escape broke URL parsing.
		authority := s
		if _, tail, ok := strings.Cut(authority, "://"); ok {
			authority = tail
		}
		authority, _, _ = strings.Cut(authority, "/")
		host, _ := url.Parse("https://" + authority)
		if host != nil && strings.EqualFold(host.Hostname(), "open.spotify.com") {
			return "", "", true, ErrSpotifyURL
		}
		return
	}
	if u.Host == "" {
		bare := s
		malformedScheme := u.Scheme != ""
		if malformedScheme {
			bare = strings.TrimLeft(u.Opaque+u.Path, "/")
		}
		parsed, e := url.Parse("https://" + bare)
		if e != nil || !strings.EqualFold(parsed.Hostname(), "open.spotify.com") {
			return
		}
		if malformedScheme {
			return "", "", true, ErrSpotifyURL
		}
		u = parsed
	}
	if !strings.EqualFold(u.Hostname(), "open.spotify.com") {
		return
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Port() != "" {
		return "", "", true, ErrSpotifyURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 3 && strings.HasPrefix(parts[0], "intl-") {
		parts = parts[1:]
	}
	if len(parts) != 2 || (parts[0] != "track" && parts[0] != "playlist" && parts[0] != "album") || !spotifyID.MatchString(parts[1]) {
		return "", "", true, ErrSpotifyURL
	}
	return parts[0], parts[1], true, nil
}

func (r CollectionResolver) Resolve(ctx context.Context, query string) (Collection, error) {
	if len(query) > 500 {
		return Collection{}, errors.New("query too long")
	}
	source, kind, id, err := CollectionURL(query)
	if err != nil {
		return Collection{}, err
	}
	if source == "" {
		track, err := r.YouTube.Resolve(ctx, query)
		if err != nil {
			return Collection{}, err
		}
		return Collection{Tracks: []Track{track}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if source == "youtube" {
		return r.youtube(ctx, id)
	}
	result, err := r.spotifyMetadata(ctx, kind, id)
	if err != nil {
		return Collection{}, err
	}

	// Fixed workers bound subprocess concurrency. Each slot retains source order.
	matched := make([]Track, len(result.Tracks))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				original := result.Tracks[i]
				track, err := r.MatchSpotify(ctx, original, false)
				if err != nil {
					if r.Log != nil {
						r.Log.Warn("Spotify track match failed", "stage", "spotify_match", "title", original.Title, "artist", original.Artist, "error", err)
					}
					continue
				}

				matched[i] = track
			}
		}()
	}
dispatch:
	for i := range result.Tracks {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return Collection{}, ctx.Err()
	}
	result.Tracks = nil
	for _, t := range matched {
		if t.URL == "" {
			result.Skipped++
		} else {
			result.Tracks = append(result.Tracks, t)
		}
	}
	if len(result.Tracks) == 0 {
		if kind == "track" {
			return Collection{}, ErrSpotifyMatch
		}
		return Collection{}, ErrSpotifyMatch
	}
	result.IsCollection = kind != "track"
	return result, nil
}

func (r CollectionResolver) youtube(ctx context.Context, id string) (Collection, error) {
	target := "https://www.youtube.com/playlist?list=" + id
	args := append(BaseArgs(), "--yes-playlist", "--flat-playlist", "--playlist-end", "501", "--skip-download", "--dump-single-json", "--ignore-errors", "--", target)
	cmd := Command(ctx, r.YouTube.Binary, args...)
	out, stderr := &LimitedBuffer{Limit: 8 << 20}, &LimitedBuffer{Limit: 8192}
	cmd.Stdout, cmd.Stderr = out, stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return Collection{}, ctx.Err()
	}
	if out.Truncated {
		return Collection{}, errors.New("playlist metadata exceeded size limit")
	}
	var data struct {
		Title   string       `json:"title"`
		Entries []*Candidate `json:"entries"`
		Count   int          `json:"playlist_count"`
	}
	if err := json.Unmarshal(out.Data, &data); err != nil {
		return Collection{}, fmt.Errorf("playlist extraction: %v; decode: %w; %s", runErr, err, stderr.Data)
	}
	// yt-dlp may exit nonzero despite producing usable entries with --ignore-errors.
	if runErr != nil && r.Log != nil {
		r.Log.Warn("partial YouTube playlist extraction", "error", runErr, "details", string(stderr.Data))
	}
	if len(data.Entries) > MaxCollectionTracks || data.Count > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	result := Collection{Title: data.Title, SourceURL: target, IsCollection: true}
	for _, c := range data.Entries {
		if c == nil || !videoID.MatchString(c.ID) || c.Title == "" || c.Title == "[Private video]" || c.Title == "[Deleted video]" || c.Availability == "private" || c.Availability == "premium_only" || c.Availability == "subscriber_only" || c.LiveStatus == "is_upcoming" {
			result.Skipped++
			continue
		}
		result.Tracks = append(result.Tracks, Track{Artist: c.Artist, Artists: c.Artists, Album: c.Album, YouTubeThumbnailURL: SafeArtworkURL(c.Thumbnail), Live: c.Live || c.LiveStatus == "is_live", Title: c.Title, Channel: c.Channel, VideoID: c.ID, URL: "https://www.youtube.com/watch?v=" + c.ID, Duration: time.Duration(c.Duration * float64(time.Second)), RequestedSource: "youtube", VerifyBeforePlay: true})
	}
	if len(result.Tracks) == 0 {
		return Collection{}, ErrNoTracks
	}
	return result, nil
}

// Album version labels can distinguish a live release whose track title omits
// "live". Do not add the entire album name as unrelated song-title tokens.
func spotifyQuery(t Track) string {
	query := t.Artist + " " + t.Title
	for _, artist := range t.Artists {
		if artist != "" && !has(normalized(query), normalized(artist)) {
			query += " " + artist
		}
	}
	for _, variant := range variants {
		if has(t.Album, variant) && !has(query, variant) {
			query += " " + variant
		}
	}
	return query
}
