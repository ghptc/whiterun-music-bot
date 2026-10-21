package media

import (
	"context"
	"time"
)

// CatalogStore contains provider metadata only; mappings retain identity evidence.
// It is global; requester, queue positions and sessions are never catalog data.
type CatalogStore interface {
	LoadCatalog(context.Context, string) (Track, bool, error)
	SaveCatalog(context.Context, Track) error
}

func catalogContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 200*time.Millisecond)
}
func (r CollectionResolver) saveCatalog(ctx context.Context, t Track) {
	store, ok := r.YouTube.Mappings.(CatalogStore)
	if !ok || t.SpotifyID == "" || t.MetadataPending {
		return
	}
	bounded, cancel := catalogContext(ctx)
	defer cancel()
	started := time.Now()
	err := store.SaveCatalog(bounded, t)
	TimingFrom(ctx).Event("catalog_write", started, "success", err == nil)
	if err != nil && r.Log != nil {
		r.Log.Warn("catalog_write_failed", "error", err)
	}
}

// One transaction avoids a durable commit per playlist occurrence. The whole
// optional cache write has one deadline; queue preparation never waits N times.
func (r CollectionResolver) saveCatalogTracks(ctx context.Context, tracks []Track) {
	complete := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		if !t.MetadataPending {
			complete = append(complete, t)
		}
	}
	tracks = complete
	if len(tracks) == 0 {
		return
	}
	if store, ok := r.YouTube.Mappings.(interface {
		SaveCatalogBatch(context.Context, []Track) error
	}); ok && len(tracks) > 1 {
		bounded, cancel := catalogContext(ctx)
		defer cancel()
		started := time.Now()
		err := store.SaveCatalogBatch(bounded, tracks)
		TimingFrom(ctx).Event("catalog_batch_write", started, "tracks", len(tracks), "success", err == nil)
		if err != nil && r.Log != nil {
			r.Log.Warn("catalog_write_failed", "error", err)
		}
		return
	}
	bounded, cancel := catalogContext(ctx)
	defer cancel()
	for _, t := range tracks {
		if bounded.Err() != nil {
			return
		}
		r.saveCatalog(bounded, t)
	}
}

// PlaylistCatalogStore persists occurrences separately from track metadata.
type PlaylistCatalogStore interface {
	LoadPlaylist(context.Context, string) (Collection, bool, error)
	SavePlaylist(context.Context, string, Collection) error
}
