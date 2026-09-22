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
	Search func(context.Context, string) (Track, error)
}

var collectionID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var spotifyID = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

// CollectionURL canonicalizes only supported provider URLs before external calls.
func CollectionURL(query string) (source, kind, id string, err error) {
	s := strings.TrimSpace(query)
	if strings.HasPrefix(s, "open.spotify.com/") || strings.HasPrefix(s, "youtube.com/") || strings.HasPrefix(s, "www.youtube.com/") || strings.HasPrefix(s, "music.youtube.com/") || strings.HasPrefix(s, "m.youtube.com/") || strings.HasPrefix(s, "youtu.be/") {
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
		if list := u.Query().Get("list"); list != "" && (u.Path == "/playlist" || u.Path == "/watch" || (u.Hostname() == "youtu.be" && videoID.MatchString(strings.TrimPrefix(u.Path, "/")))) {
			if !collectionID.MatchString(list) {
				return "", "", "", errors.New("invalid playlist ID")
			}
			return "youtube", "playlist", list, nil
		}
	case "open.spotify.com":
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 3 && strings.HasPrefix(parts[0], "intl-") {
			parts = parts[1:]
		}
		if len(parts) != 2 || (parts[0] != "playlist" && parts[0] != "album") || !spotifyID.MatchString(parts[1]) {
			return "", "", "", errors.New("unsupported Spotify URL")
		}
		return "spotify", parts[0], parts[1], nil
	}
	return
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
	if r.Spotify == nil {
		return Collection{}, ErrSpotifyConfig
	}
	result, err := r.Spotify.Collection(ctx, kind, id)
	if err != nil {
		return Collection{}, err
	}
	if len(result.Tracks) > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	search := r.Search
	if search == nil {
		search = r.YouTube.Resolve
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
				track, err := search(ctx, original.Artist+" "+original.Title)
				if err != nil {
					if r.Log != nil {
						r.Log.Warn("Spotify track match failed", "title", original.Title, "artist", original.Artist, "error", err)
					}
					continue
				}
				track.Title, track.Artist = original.Title, original.Artist
				track.RequestedSource, track.SourceURL = "spotify", original.SourceURL
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
		return Collection{}, ErrNoTracks
	}
	result.IsCollection = true
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
		if c == nil || !videoID.MatchString(c.ID) || c.Title == "" || c.Title == "[Private video]" || c.Title == "[Deleted video]" || c.Availability == "private" || c.Availability == "premium_only" || c.Availability == "subscriber_only" || c.Live || c.LiveStatus == "is_upcoming" || c.LiveStatus == "is_live" {
			result.Skipped++
			continue
		}
		result.Tracks = append(result.Tracks, Track{Title: c.Title, Channel: c.Channel, VideoID: c.ID, URL: "https://www.youtube.com/watch?v=" + c.ID, Duration: time.Duration(c.Duration * float64(time.Second)), RequestedSource: "youtube", VerifyBeforePlay: true})
	}
	if len(result.Tracks) == 0 {
		return Collection{}, ErrNoTracks
	}
	return result, nil
}
