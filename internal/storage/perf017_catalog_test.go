package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"whiterun/internal/media"
)

func TestCatalogBatchAtomicAndPreservesArtwork(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	original := catalogTrack()
	if err = db.SaveCatalog(ctx, original); err != nil {
		t.Fatal(err)
	}
	update := original
	update.Title = "Updated"
	update.SpotifyArtworkURL = ""
	if err = db.SaveCatalogBatch(ctx, []media.Track{update, {}}); err == nil {
		t.Fatal("invalid batch committed")
	}
	got, _, err := db.LoadCatalog(ctx, original.SpotifyID)
	if err != nil || got.Title != original.Title {
		t.Fatal(got, err)
	}
	if err = db.SaveCatalogBatch(ctx, []media.Track{update}); err != nil {
		t.Fatal(err)
	}
	got, _, err = db.LoadCatalog(ctx, original.SpotifyID)
	if err != nil || got.Title != "Updated" || got.SpotifyArtworkURL != original.SpotifyArtworkURL {
		t.Fatal(got, err)
	}
}

func BenchmarkCatalogCollection(b *testing.B) {
	for _, n := range []int{10, 302} {
		for _, batch := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/batch=%t", n, batch), func(b *testing.B) {
				db, err := Open(filepath.Join(b.TempDir(), "catalog.db"))
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				tracks := make([]media.Track, n)
				for i := range tracks {
					tracks[i] = catalogTrack()
					tracks[i].SpotifyID = fmt.Sprintf("%022d", i)
				}
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if batch {
						err = db.SaveCatalogBatch(ctx, tracks)
					} else {
						for _, t := range tracks {
							if err = db.SaveCatalog(ctx, t); err != nil {
								break
							}
						}
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
