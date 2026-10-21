package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"whiterun/internal/media"
)

func qaPlaylist(n int) media.Collection {
	c := media.Collection{Title: "Música 日本語 🎵", SourceURL: "https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg", IsCollection: true}
	for i := range n {
		t := catalogTrack()
		t.SpotifyID = fmt.Sprintf("%022d", i)
		t.Title = fmt.Sprintf("Déjà Vu %d (Live)", i)
		t.Artists = []string{"Beyoncé", "宇多田ヒカル"}
		t.Position = i + 1
		t.Total = n
		c.Tracks = append(c.Tracks, t)
	}
	return c
}
func assertCatalogIntegrity(t *testing.T, db *DB) {
	t.Helper()
	rows, err := db.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
func TestDB022SchemaUpgradeAndDisposableColumnMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Build a real pre-catalog schema, then upgrade it through production Open.
	for _, name := range []string{"001_initial.sql", "002_playback_events.sql"} {
		body, e := migrations.ReadFile("migrations/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = old.Exec(string(body)); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = old.Exec(`CREATE TABLE migrations(version INTEGER PRIMARY KEY); INSERT INTO migrations VALUES(1),(2); INSERT INTO guilds VALUES('old','{}','{"loop":"queue"}')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.db.QueryRow("SELECT state FROM guilds WHERE guild_id='old'").Scan(&state); err != nil || state != `{"loop":"queue"}` {
		t.Fatal(state, err)
	}
	for table, want := range map[string][]string{"music_catalog": {"spotify_id", "metadata", "updated_at"}, "catalog_positions": {"playlist_id", "ordinal", "position", "total", "track_id"}, "catalog_playlists": {"spotify_id", "title", "source_url", "skipped"}} {
		rows, e := db.db.Query("PRAGMA table_info(" + table + ")")
		if e != nil {
			t.Fatal(e)
		}
		var got []string
		for rows.Next() {
			var cid, nn, pk int
			var name, typ string
			var def any
			if e = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); e != nil {
				t.Fatal(e)
			}
			got = append(got, name)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Fatal(table, got)
		}
	}
	for _, name := range []string{"catalog_positions_track", "catalog_artist_title", "catalog_activity_window"} {
		var ddl string
		if err = db.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&ddl); err != nil || ddl == "" {
			t.Fatal(name, ddl, err)
		}
	}
	// Test-only rebuild supports add/rename/drop without changing production fields.
	for _, q := range []string{`CREATE TABLE qa_fields(id INTEGER PRIMARY KEY,title TEXT NOT NULL UNIQUE,note TEXT)`, `INSERT INTO qa_fields VALUES(1,'日本語',NULL)`, `ALTER TABLE qa_fields ADD COLUMN optional TEXT`, `UPDATE qa_fields SET optional='á'`, `ALTER TABLE qa_fields RENAME COLUMN optional TO renamed`, `CREATE INDEX qa_title ON qa_fields(title)`} {
		if _, err = db.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE qa_rebuilt(id INTEGER PRIMARY KEY,title TEXT NOT NULL UNIQUE,renamed TEXT)`, `INSERT INTO qa_rebuilt SELECT id,title,renamed FROM qa_fields`, `DROP TABLE qa_fields`, `ALTER TABLE qa_rebuilt RENAME TO qa_fields`, `CREATE INDEX qa_title ON qa_fields(title)`} {
		if _, err = tx.Exec(q); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var title, value string
	if err = db.db.QueryRow("SELECT title,renamed FROM qa_fields WHERE id=1").Scan(&title, &value); err != nil || title != "日本語" || value != "á" {
		t.Fatal(title, value, err)
	}
	if _, err = db.db.Exec("INSERT INTO qa_fields VALUES(2,'日本語',NULL)"); err == nil {
		t.Fatal("unique constraint lost")
	}
	tx, err = db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("ALTER TABLE qa_fields ADD COLUMN rolled_back TEXT"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec("SELECT rolled_back FROM qa_fields"); err == nil {
		t.Fatal("DDL rollback failed")
	}

	for _, q := range []string{`INSERT INTO music_catalog VALUES(NULL,'{}',1)`, `INSERT INTO catalog_positions VALUES('missing',0,1,1,'missing')`, `INSERT INTO catalog_playlists VALUES('invalid',NULL,'',0)`} {
		if _, err = db.db.Exec(q); err == nil {
			t.Fatal("invalid schema write accepted", q)
		}
	}
	assertCatalogIntegrity(t, db)
}

func TestDB022PlaylistCRUDRestartAtomicityAndConcurrency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original := qaPlaylist(10)
	original.Tracks[3] = original.Tracks[1]
	original.Tracks[3].Position = 4
	original.Tracks[5].Album = ""
	original.Tracks[5].Artists = nil
	original.Tracks[5].Explicit = nil
	original.Tracks[7].Title = "Déjà Vu (Studio)"
	original.Tracks[8].Title = "Déjà Vu (Live)"
	if err = db.SavePlaylist(ctx, "one", original); err != nil {
		t.Fatal(err)
	}
	if err = db.SavePlaylist(ctx, "shared", original); err != nil {
		t.Fatal(err)
	}
	broken := qaPlaylist(10)
	broken.Title = "must rollback"
	broken.Tracks[7].MetadataPending = true
	if err = db.SavePlaylist(ctx, "one", broken); err == nil {
		t.Fatal("partial import committed")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, hit, err := db.LoadPlaylist(ctx, "one")
	if err != nil || !hit {
		t.Fatal(hit, err)
	}
	for i, v := range got.Tracks {
		want := original.Tracks[i]
		if v.SpotifyID != want.SpotifyID || v.Title != want.Title || v.Position != want.Position || !reflect.DeepEqual(v.Artists, want.Artists) || v.RequestedBy != "" {
			t.Fatal(i, v, want)
		}
	}
	if got.Title != original.Title {
		t.Fatal(got.Title)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 10 {
				if e := db.SavePlaylist(ctx, "one", original); e != nil {
					t.Error(e)
				}
			}
		})
		wg.Go(func() {
			for range 20 {
				c, ok, e := db.LoadPlaylist(ctx, "one")
				if e != nil || !ok || len(c.Tracks) != 10 {
					t.Error(len(c.Tracks), ok, e)
				}
			}
		})
	}
	wg.Wait()
	// Separate real SQLite handles exercise writer serialization across connections.
	other, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	wg.Go(func() {
		for range 10 {
			if e := other.DeletePlaylist(ctx, "one"); e != nil {
				t.Error(e)
			}
			if e := other.SavePlaylist(ctx, "one", original); e != nil {
				t.Error(e)
			}
		}
	})
	wg.Go(func() {
		for range 40 {
			c, hit, e := db.LoadPlaylist(ctx, "one")
			if e != nil || (hit && len(c.Tracks) != 10) || (!hit && len(c.Tracks) != 0) {
				t.Error("torn deletion/read snapshot", len(c.Tracks), hit, e)
			}
		}
	})
	wg.Wait()
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteOrphanTrack(ctx, original.Tracks[1].SpotifyID); err == nil {
		t.Fatal("referenced track deleted")
	}
	if err = db.DeletePlaylistPosition(ctx, "one", 2); err != nil {
		t.Fatal(err)
	}
	got, _, err = db.LoadPlaylist(ctx, "one")
	if err != nil || len(got.Tracks) != 9 || got.Tracks[2].SpotifyID != original.Tracks[1].SpotifyID {
		t.Fatal(got, err)
	}
	if err = db.DeletePlaylist(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, hit, err = db.LoadCatalog(ctx, original.Tracks[0].SpotifyID); err != nil || !hit {
		t.Fatal("shared music deleted", hit, err)
	}
	if err = db.DeletePlaylist(ctx, "shared"); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteOrphanTrack(ctx, original.Tracks[0].SpotifyID); err != nil {
		t.Fatal(err)
	}
	if _, hit, err = db.LoadCatalog(ctx, original.Tracks[0].SpotifyID); err != nil || hit {
		t.Fatal(hit, err)
	}
	assertCatalogIntegrity(t, db)
}

type qaProvider struct {
	c     media.Collection
	calls int
	fail  bool
}

func (p *qaProvider) Collection(context.Context, string, string) (media.Collection, error) {
	p.calls++
	if p.fail {
		return media.Collection{}, fmt.Errorf("Spotify forbidden during SQLite reconstruction")
	}
	return p.c, nil
}
func TestDB022OfflinePlaylistResolver(t *testing.T) { qaOfflinePlaylist(t, qaPlaylist(123)) }
func qaOfflinePlaylist(t *testing.T, c media.Collection) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := &qaProvider{c: c}
	r := media.CollectionResolver{Spotify: provider, YouTube: media.Resolver{Mappings: db}}
	first, _, err := r.Prepare(ctx, c.SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	forbidden := &qaProvider{fail: true}
	r = media.CollectionResolver{Spotify: forbidden, YouTube: media.Resolver{Mappings: db, Binary: "/nonexistent"}}
	after, _, err := r.Prepare(ctx, c.SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tracks) != len(first.Tracks) || forbidden.calls != 0 {
		t.Fatal(len(after.Tracks), len(first.Tracks), forbidden.calls)
	}
	for i, v := range after.Tracks {
		want := first.Tracks[i]
		if v.SpotifyID != want.SpotifyID || v.Title != want.Title || v.Position != want.Position || v.Total != want.Total || v.Album != want.Album || !reflect.DeepEqual(v.Artists, want.Artists) {
			t.Fatalf("position %d: %+v != %+v", i, v, want)
		}
	}
	if err = qaQueue(ctx, after.Tracks); err != nil {
		t.Fatal(err)
	}
	// Playback source availability is a separate concern.
	t.Logf("persisted=%d reconstructed=%d Spotify_cache_hit_calls=%d", len(first.Tracks), len(after.Tracks), forbidden.calls)
}
func TestDB022HomologationPlaylistLive(t *testing.T) {
	if os.Getenv("DB022_SPOTIFY_LIVE") != "1" {
		t.Skip("DB022_SPOTIFY_LIVE=1 imports public homologation playlist")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := (&media.Spotify{}).PublicPlaylist(ctx, "35m4wmHSlcS2RNsafX4dEg")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) == 0 || len(c.Tracks)+c.Skipped != c.Tracks[0].Total {
		t.Fatal("incomplete source snapshot", len(c.Tracks), c.Skipped)
	}
	qaOfflinePlaylist(t, c)
}

func TestDB022LogicalQuotaAtomicConcurrentAndWALLimitation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "quota.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Use actual serialized bytes, not a fabricated usage monitor.
	track := catalogTrack()
	if err = db.SaveCatalog(ctx, track); err != nil {
		t.Fatal(err)
	}
	u, err := db.CatalogUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetCatalogQuota(ctx, u.Used); err != nil {
		t.Fatal(err)
	}
	updated := track
	updated.Title += strings.Repeat("x", 100)
	if err = db.SaveCatalog(ctx, updated); err == nil {
		t.Fatal("over-quota update accepted")
	}
	got, _, err := db.LoadCatalog(ctx, track.SpotifyID)
	if err != nil || got.Title != track.Title {
		t.Fatal("rejected write not rolled back", got, err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			v := track
			v.SpotifyID = fmt.Sprintf("%022d", i)
			if e := db.SaveCatalog(ctx, v); e == nil {
				t.Error("concurrent over-quota write accepted")
			}
		})
	}
	wg.Wait()
	after, err := db.CatalogUsage(ctx)
	if err != nil || after.Used != u.Used || after.Used > after.Limit {
		t.Fatal(after, err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= after.Limit {
		t.Fatal("fixture must demonstrate physical WAL overhead exceeds payload quota")
	}
	t.Logf("logical quota enforced: used=%d limit=%d; physical WAL=%d: filesystem enforcement still required", after.Used, after.Limit, info.Size())
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, hit, e := db.LoadCatalog(ctx, track.SpotifyID); e != nil || !hit {
		t.Fatal(hit, e)
	}
	if err = db.SaveCatalog(ctx, updated); err == nil {
		t.Fatal("quota lost across restart")
	}
}

func TestDB022AlertWindowTiesPendingAndTransactionalRearm(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	// The title dominates the snapshot so replacement temporarily removes >15%
	// of accounted bytes; the detector must only observe committed transaction state.
	c := qaPlaylist(2)
	c.Title = strings.Repeat("á", 3000)
	if err := db.SavePlaylist(ctx, "snapshot", c); err != nil {
		t.Fatal(err)
	}
	usage, err := db.CatalogUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetCatalogQuota(ctx, usage.Used*10/9); err != nil {
		t.Fatal(err)
	}
	var before int
	if err = db.db.QueryRow("SELECT count(*) FROM catalog_alerts").Scan(&before); err != nil || before != 1 {
		t.Fatal(before, err)
	}
	for range 3 {
		if err = db.SavePlaylist(ctx, "snapshot", c); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.db.QueryRow("SELECT count(*) FROM catalog_alerts").Scan(&count); err != nil || count != before {
		t.Fatal("transient usage rearmed detector", count, before, err)
	}
	monitor := CatalogAlertMonitor{DB: db}
	if err = monitor.Poll(ctx, now); err == nil {
		t.Fatal("missing channel must leave pending attempt")
	}
	var attempts, delivered int
	var retry int64
	if err = db.db.QueryRow("SELECT attempts,delivered,retry_at FROM catalog_alerts").Scan(&attempts, &delivered, &retry); err != nil || attempts != 1 || delivered != 0 || retry <= now.Unix() {
		t.Fatal(attempts, delivered, retry, err)
	}
	if err = monitor.Poll(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id, g, c string
		at       time.Time
	}{{"a", "2", "20", now}, {"b", "1", "30", now}, {"c", "1", "20", now}, {"old", "1", "20", now.Add(-30 * 24 * time.Hour)}, {"future", "1", "20", now.Add(time.Second)}} {
		if err = db.RecordCatalogCommand(ctx, entry.id, entry.g, entry.c, entry.at); err != nil {
			t.Fatal(err)
		}
	}
	channels, err := db.CatalogChannels(ctx, now)
	if err != nil || len(channels) != 3 || channels[0].Guild != "1" || channels[0].Channel != "20" || channels[0].Commands != 1 || channels[1].Channel != "30" {
		t.Fatal(channels, err)
	}
	assertCatalogIntegrity(t, db)
}

func TestDB022ResolverPropagatesDurableImportFailure(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.SetCatalogQuota(ctx, 1); err != nil {
		t.Fatal(err)
	}
	c := qaPlaylist(10)
	source := &qaProvider{c: c}
	r := media.CollectionResolver{Spotify: source, YouTube: media.Resolver{Mappings: db}}
	if _, _, err := r.Prepare(ctx, c.SourceURL); err == nil {
		t.Fatal("import reported success without a durable snapshot")
	}
	if _, hit, err := db.LoadPlaylist(ctx, "35m4wmHSlcS2RNsafX4dEg"); err != nil || hit {
		t.Fatal("failed import leaked snapshot", hit, err)
	}
	usage, err := db.CatalogUsage(ctx)
	if err != nil || usage.Used != 0 {
		t.Fatal(usage, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	source.fail = true
	before := source.calls
	if _, _, err = r.Prepare(ctx, c.SourceURL); err == nil {
		t.Fatal("closed database error lost")
	}
	if source.calls != before {
		t.Fatal("database failure caused external Spotify fallback")
	}
}

func TestDB022QuotaLastSlotAcrossConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-quota.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := catalogTrack()
	if err = db.SaveCatalog(ctx, original); err != nil {
		t.Fatal(err)
	}
	initial, err := db.CatalogUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetCatalogQuota(ctx, initial.Used*2); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			writer := db
			if i%2 == 1 {
				writer = other
			}
			track := original
			track.SpotifyID = fmt.Sprintf("%022d", i)
			results <- writer.SaveCatalog(ctx, track)
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("expected exactly one remaining slot", success)
	}
	usage, err := db.CatalogUsage(ctx)
	if err != nil || usage.Used != usage.Limit || usage.Used != initial.Used*2 {
		t.Fatal(usage, err)
	}
	var count int
	if err = db.db.QueryRow("SELECT count(*) FROM music_catalog").Scan(&count); err != nil || count != 2 {
		t.Fatal("rejected writes leaked", count, err)
	}
	assertCatalogIntegrity(t, db)
}
