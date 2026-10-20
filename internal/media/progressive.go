package media

import (
	"context"
	"errors"
	"strings"
	"time"
)

// MatchLimiter bounds Spotify searches across guilds and collections. Background
// work can occupy only three slots, leaving immediate capacity for a first track.
type MatchLimiter struct{ all, background chan struct{} }

func NewMatchLimiter() *MatchLimiter {
	return &MatchLimiter{make(chan struct{}, 4), make(chan struct{}, 3)}
}
func (l *MatchLimiter) acquire(ctx context.Context, first bool) (func(), error) {
	if l == nil {
		return func() {}, ctx.Err()
	}
	if !first {
		select {
		case l.background <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case l.all <- struct{}{}:
		return func() {
			<-l.all
			if !first {
				<-l.background
			}
		}, nil
	case <-ctx.Done():
		if !first {
			<-l.background
		}
		return nil, ctx.Err()
	}
}

// Prepare returns complete Spotify playlist/album metadata, without YouTube work.
// All other inputs retain the existing fully-resolved single/YouTube path.
func (r CollectionResolver) Prepare(ctx context.Context, query string) (Collection, bool, error) {
	if len(query) > 500 {
		return Collection{}, false, errors.New("query too long")
	}
	source, kind, id, err := CollectionURL(query)
	if err != nil {
		return Collection{}, false, err
	}
	if source != "spotify" || kind == "track" {
		c, err := r.Resolve(ctx, query)
		return c, false, err
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c, err := r.spotifyMetadata(ctx, kind, id)
	if err != nil {
		return Collection{}, false, err
	}
	TimingFrom(ctx).Event("collection_metadata_complete", started, "spotify_tracks", len(c.Tracks), "metadata_skipped", c.Skipped)
	return c, true, nil
}
func (r CollectionResolver) spotifyMetadata(ctx context.Context, kind, id string) (Collection, error) {

	if store, ok := r.YouTube.Mappings.(CatalogStore); ok && kind == "track" {
		started := time.Now()
		bounded, cancel := catalogContext(ctx)
		t, hit, cacheErr := store.LoadCatalog(bounded, id)
		cancel()
		TimingFrom(ctx).Event("catalog_lookup", started, "hit", hit, "success", cacheErr == nil, "external_metadata_calls_avoided", map[bool]int{true: 1, false: 0}[hit && cacheErr == nil])
		if cacheErr != nil && r.Log != nil {
			r.Log.Warn("catalog_read_failed", "error", cacheErr)
		}
		if cacheErr == nil && hit && t.SpotifyID == id && t.RequestedSource == "spotify" {
			t.Position, t.Total = 1, 1
			return Collection{Tracks: []Track{t}, SourceURL: t.SourceURL}, nil
		}
	}
	if r.Spotify == nil {
		return Collection{}, ErrSpotifyConfig
	}
	c, err := r.Spotify.Collection(ctx, kind, id)
	if err != nil {
		return Collection{}, err
	}
	if err = ctx.Err(); err != nil {
		return Collection{}, err
	}
	if len(c.Tracks) == 0 {
		return Collection{}, ErrNoTracks
	}
	if len(c.Tracks) > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	r.restoreCatalogArtwork(ctx, c.Tracks)
	r.saveCatalogTracks(ctx, c.Tracks)
	for i := range c.Tracks {
		if c.Tracks[i].Position == 0 {
			c.Tracks[i].Position, c.Tracks[i].Total = i+1, len(c.Tracks)+c.Skipped
		}
	}
	c.IsCollection = kind != "track"
	return c, nil
}

// MatchSpotify uses the same canonical resolver/ranking as the synchronous path.
func (r CollectionResolver) MatchSpotify(ctx context.Context, original Track, first bool) (Track, error) {
	release, err := r.Limiter.acquire(ctx, first)
	if err != nil {
		return Track{}, err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return Track{}, err
	}
	search := r.Search
	if search == nil {
		resolver := r.YouTube
		resolver.Match = &original
		search = resolver.Resolve
	}
	track, err := search(ctx, spotifyQuery(original))
	if err != nil {
		return Track{}, err
	}
	if track.URL == "" {
		return Track{}, ErrSpotifyMatch
	}
	// Spotify remains the identity source; only audio locator fields come from YouTube.
	original.VideoID, original.URL, original.Channel = track.VideoID, track.URL, track.Channel
	original.YouTubeThumbnailURL = track.YouTubeThumbnailURL
	original.identityProof = track.identityProof
	original.RequestedSource = "spotify"
	if original.SpotifyID == "" {
		_, original.SpotifyID, _, _ = spotifyURL(original.SourceURL)
	}
	original.MatchStatus, original.MatchReason = MatchConfirmed, "verified_metadata_match"
	original.Timing = TimingFrom(ctx)
	track = original
	return track, nil
}

// PreparePlay preserves the ordinary path unless direct selection is explicit.
func (r CollectionResolver) PreparePlay(ctx context.Context, query string, force bool) (Collection, bool, error) {
	if !force {
		return r.Prepare(ctx, query)
	}
	if len(query) > 500 {
		return Collection{}, false, ErrForceURL
	}
	direct, ok, err := YouTubeURL(strings.TrimSpace(query))
	if err != nil || !ok {
		return Collection{}, false, ErrForceURL
	}
	resolver := r.YouTube
	// A manual selection must never inherit Spotify identity or cache keys.
	resolver.Match = nil
	track, err := resolver.Resolve(ctx, direct)
	if err != nil {
		return Collection{}, false, err
	}
	if track.URL != direct || "https://www.youtube.com/watch?v="+track.VideoID != direct {
		return Collection{}, false, ErrNoSong
	}
	track.ForcedYouTube = true
	return Collection{Tracks: []Track{track}}, false, nil
}

var ErrForceURL = errors.New("Force requires a direct YouTube video URL. Spotify, searches and playlists are not supported.")
