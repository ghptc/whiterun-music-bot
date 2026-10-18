package media

import (
	"context"
	"errors"
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
	track.Title, track.Artist, track.Album = original.Title, original.Artist, original.Album
	track.Artists = append([]string(nil), original.Artists...)
	track.RequestedSource, track.SourceURL = "spotify", original.SourceURL
	track.Timing = TimingFrom(ctx)
	return track, nil
}
