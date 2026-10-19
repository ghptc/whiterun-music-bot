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
	if !ok || t.SpotifyID == "" {
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
