package media

import (
	"context"
	"time"
)

type playbackMetadataKey struct{}

func playbackMetadata(ctx context.Context) bool {
	value, _ := ctx.Value(playbackMetadataKey{}).(bool)
	return value
}

// CollectionForPlayback keeps the complete occurrence manifest and revision
// checks, but defers tail enrichment to the player's bounded logical window.
// Collection retains its synchronous, fully hydrated contract for other callers.
func (s *Spotify) CollectionForPlayback(ctx context.Context, kind, id string) (Collection, error) {
	return s.Collection(context.WithValue(ctx, playbackMetadataKey{}, true), kind, id)
}

func (s *Spotify) TrackMetadata(ctx context.Context, id string) (Track, error) {
	if !spotifyID.MatchString(id) {
		return Track{}, ErrSpotifyURL
	}
	body, err := s.spotifyPublicPage(ctx, "embed/track/"+id)
	if err != nil {
		return Track{}, err
	}
	return parseSpotifyTrackEmbed(body, "spotify:track:"+id)
}

// Enrichment shares the existing foreground/background admission bound. No
// metadata-only occurrence can become a YouTube search or a confirmed match.
func (r CollectionResolver) enrichSpotify(ctx context.Context, original Track) (Track, error) {
	started := time.Now()
	var track Track
	hit := false
	if store, ok := r.YouTube.Mappings.(CatalogStore); ok {
		bounded, cancel := catalogContext(ctx)
		cached, found, err := store.LoadCatalog(bounded, original.SpotifyID)
		cancel()
		if err == nil && found && completeSpotifyMetadata(cached, original.SpotifyID) {
			track, hit = cached, true
		}
	}
	if !hit {
		provider, ok := r.Spotify.(interface {
			TrackMetadata(context.Context, string) (Track, error)
		})
		if !ok {
			return Track{}, ErrSpotifyConfig
		}
		var err error
		track, err = provider.TrackMetadata(ctx, original.SpotifyID)
		if err != nil {
			return Track{}, err
		}
		if !completeSpotifyMetadata(track, original.SpotifyID) {
			return Track{}, ErrSpotifyIncomplete
		}
		r.saveCatalog(ctx, track)
	}
	track.Position, track.Total = original.Position, original.Total
	track.RequestedBy, track.Timing = original.RequestedBy, original.Timing
	TimingFrom(ctx).Event("spotify_track_enrichment", started, "cache_hit", hit)
	return track, ctx.Err()
}
func completeSpotifyMetadata(t Track, id string) bool {
	return spotifyID.MatchString(id) && t.SpotifyID == id && t.RequestedSource == "spotify" && !t.MetadataPending && t.Title != "" && t.Artist != "" && t.SourceURL == "https://open.spotify.com/track/"+id
}
