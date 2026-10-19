package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func catalogTrack() media.Track {
	return media.Track{SpotifyID: "1234567890123456789012", RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/1234567890123456789012", Title: "Song", Artist: "Artist", Artists: []string{"Artist"}, Album: "Album", Duration: 210 * time.Second, ISRC: "ISRC", SpotifyArtworkURL: "https://i.scdn.co/image/cover", RequestedBy: "private", Position: 30, Total: 100}
}

type noSpotify struct{ calls int }

func (s *noSpotify) Collection(context.Context, string, string) (media.Collection, error) {
	s.calls++
	return media.Collection{}, fmt.Errorf("external metadata must not run")
}

func TestCatalogReuseAcrossRestartGuildsAndMappingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	ctx := context.Background()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := catalogTrack()
	candidate := media.Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist - Topic", Duration: 210, CheckedAt: time.Now(), Thumbnail: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}
	if err = db.SaveCatalog(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveMapping(ctx, media.MappingKey(original, nil), candidate); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err = db.SaveGuild(ctx, id, player.DurableState{Queue: []media.Track{{Title: id}}}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := &noSpotify{}
	resolver := media.CollectionResolver{Spotify: provider, YouTube: media.Resolver{Mappings: db, Binary: "/nonexistent/cr002"}}
	for _, guild := range []string{"one", "two"} {
		c, err := resolver.Resolve(ctx, "https://open.spotify.com/track/"+original.SpotifyID)
		if err != nil || len(c.Tracks) != 1 {
			t.Fatal(c, err)
		}
		got := c.Tracks[0]
		if got.Title != original.Title || got.ISRC != original.ISRC || got.SpotifyArtworkURL != original.SpotifyArtworkURL || got.VideoID != candidate.ID || got.RequestedBy != "" || got.Position != 1 {
			t.Fatal(got)
		}
		state, err := db.LoadGuild(ctx, guild)
		if err != nil || state.Queue[0].Title != guild {
			t.Fatal(state, err)
		}
	}
	if provider.calls != 0 {
		t.Fatal(provider.calls)
	}
}
func TestCatalogConcurrentUpsertExpiryConstraintsAndArtworkRefresh(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	track := catalogTrack()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := db.SaveCatalog(ctx, track); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var count int
	if err := db.db.QueryRow("SELECT count(*) FROM music_catalog").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	track.SpotifyArtworkURL = "https://i.scdn.co/image/new"
	if err := db.SaveCatalog(ctx, track); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.LoadCatalog(ctx, track.SpotifyID)
	if err != nil || !ok || got.SpotifyArtworkURL != track.SpotifyArtworkURL {
		t.Fatal(got, ok, err)
	}
	if _, err = db.db.Exec("UPDATE music_catalog SET updated_at=?", time.Now().Add(-25*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = db.LoadCatalog(ctx, track.SpotifyID); err != nil || ok {
		t.Fatal(ok, err)
	}
	if _, err = db.db.Exec("INSERT INTO music_catalog VALUES('', '{}',1)"); err == nil {
		t.Fatal("empty identity accepted")
	}
	if _, err = db.db.Exec("INSERT INTO music_catalog VALUES('id','invalid',1)"); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
func BenchmarkCatalogSQLiteHit(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	track := catalogTrack()
	if err = db.SaveCatalog(ctx, track); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, ok, err := db.LoadCatalog(ctx, track.SpotifyID); err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}
func BenchmarkCatalogSQLiteMiss(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	for b.Loop() {
		if _, _, err := db.LoadCatalog(context.Background(), "missing"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCatalogCacheFailuresFallbackToProviderAndResolver(t *testing.T) {
	db := openTest(t)
	db.Close()
	provider := &catalogProvider{}
	resolver := media.CollectionResolver{YouTube: media.Resolver{Mappings: db}, Spotify: provider, Search: func(context.Context, string) (media.Track, error) {
		return media.Track{URL: "https://www.youtube.com/watch?v=abcdefghijk", VideoID: "abcdefghijk"}, nil
	}}
	c, err := resolver.Resolve(context.Background(), "https://open.spotify.com/track/1234567890123456789012")
	if err != nil || len(c.Tracks) != 1 || provider.calls != 1 {
		t.Fatal(c, err, provider.calls)
	}
}

type catalogProvider struct{ calls int }

func (s *catalogProvider) Collection(context.Context, string, string) (media.Collection, error) {
	s.calls++
	return media.Collection{Tracks: []media.Track{catalogTrack()}}, nil
}

func TestOldStreamFailureCannotDeleteReplacementMapping(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	track := catalogTrack()
	track.VideoID = "oldVideo123"
	replacement := media.Candidate{ID: "abcdefghijk", CheckedAt: time.Now()}
	key := media.MappingKey(track, nil)
	if err := db.SaveMapping(ctx, key, replacement); err != nil {
		t.Fatal(err)
	}
	resolver := media.Resolver{Mappings: db}
	resolver.InvalidateSpotify(ctx, track)
	if got, ok, err := db.LoadMapping(ctx, key); err != nil || !ok || got.ID != replacement.ID {
		t.Fatal(got, ok, err)
	}
	track.VideoID = replacement.ID
	resolver.InvalidateSpotify(ctx, track)
	if _, ok, err := db.LoadMapping(ctx, key); err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestMissingProviderCoverReusesExactSpotifyCatalogIdentity(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	original := catalogTrack()
	if err := db.SaveCatalog(ctx, original); err != nil {
		t.Fatal(err)
	}
	provider := &artworkCollectionProvider{tracks: []media.Track{
		{SpotifyID: original.SpotifyID, RequestedSource: "spotify", Title: "Different display title", YouTubeThumbnailURL: "https://i.ytimg.com/new-video"},
		{SpotifyID: "unrelated", RequestedSource: "spotify", Title: original.Title},
		{VideoID: "abcdefghijk", RequestedSource: "youtube", Title: original.Title},
	}}
	resolver := media.CollectionResolver{Spotify: provider, YouTube: media.Resolver{Mappings: db}}
	collection, progressive, err := resolver.Prepare(ctx, "https://open.spotify.com/playlist/1234567890123456789012")
	if err != nil || !progressive || len(collection.Tracks) != 3 {
		t.Fatal(collection, err)
	}
	if collection.Tracks[0].SpotifyArtworkURL != original.SpotifyArtworkURL || collection.Tracks[0].YouTubeThumbnailURL != provider.tracks[0].YouTubeThumbnailURL {
		t.Fatal(collection.Tracks[0])
	}
	for _, track := range collection.Tracks[1:] {
		if track.SpotifyArtworkURL != "" {
			t.Fatal("title similarity assigned wrong cover", track)
		}
	}
	// A refresh without cover data must not erase valid persisted album art.
	stripped := original
	stripped.SpotifyArtworkURL = ""
	stripped.YouTubeThumbnailURL = "https://i.ytimg.com/selected"
	if err := db.SaveCatalog(ctx, stripped); err != nil {
		t.Fatal(err)
	}
	got, hit, err := db.LoadCatalog(ctx, original.SpotifyID)
	if err != nil || !hit || got.SpotifyArtworkURL != original.SpotifyArtworkURL || got.YouTubeThumbnailURL != stripped.YouTubeThumbnailURL {
		t.Fatal(got, hit, err)
	}
}

type artworkCollectionProvider struct{ tracks []media.Track }

func (p *artworkCollectionProvider) Collection(context.Context, string, string) (media.Collection, error) {
	return media.Collection{Tracks: append([]media.Track(nil), p.tracks...)}, nil
}
