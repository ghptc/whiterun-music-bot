package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/player"
)

type qaSilentVoice struct{}

func (qaSilentVoice) WriteFrame(context.Context, []byte) error { return nil }
func (qaSilentVoice) Speaking(context.Context, bool) error     { return nil }
func (qaSilentVoice) Close(context.Context)                    {}
func qaQueue(ctx context.Context, tracks []media.Track) error {
	p := player.New(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, _ player.Voice) error { <-ctx.Done(); return ctx.Err() })
	defer p.Close(context.Background())
	if err := p.Attach(qaSilentVoice{}); err != nil {
		return err
	}
	if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
		return err
	}
	current, queue := p.Snapshot()
	if current != nil {
		queue = append([]media.Track{*current}, queue...)
	}
	if len(queue) != len(tracks) {
		return fmt.Errorf("queue length %d, expected %d", len(queue), len(tracks))
	}
	for i, t := range queue {
		if t.SpotifyID != tracks[i].SpotifyID || t.Position != tracks[i].Position || t.Title != tracks[i].Title {
			return fmt.Errorf("queue occurrence %d differs", i)
		}
	}
	return nil
}
func TestDB022QueryPlans(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.SavePlaylist(ctx, "plan", qaPlaylist(100)); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"track":        `SELECT metadata FROM music_catalog WHERE spotify_id='id'`,
		"playlist":     `SELECT title FROM catalog_playlists WHERE spotify_id='plan'`,
		"position":     `SELECT track_id FROM catalog_positions WHERE playlist_id='plan' AND position=10`,
		"ordered":      `SELECT track_id FROM catalog_positions WHERE playlist_id='plan' ORDER BY ordinal`,
		"artist_title": `SELECT spotify_id FROM music_catalog WHERE lower(json_extract(metadata,'$.Artist'))='artist' AND lower(json_extract(metadata,'$.Title'))='title'`,
	} {
		rows, err := db.db.Query("EXPLAIN QUERY PLAN " + q)
		if err != nil {
			t.Fatal(err)
		}
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		indexed := false
		for _, d := range details {
			if len(d) > 0 && stringsContainsIndex(d) {
				indexed = true
			}
		}
		if !indexed {
			t.Fatal(name, details)
		}
		t.Log(name, details)
	}
}
func stringsContainsIndex(s string) bool {
	return strings.Contains(s, "USING INDEX") || strings.Contains(s, "USING COVERING INDEX")
}

// All operations hit SQLite directly: there is no application cache. "cold"
// releases SQLite's page cache before each measurement; the OS cache remains
// uncontrolled. It does not claim a cold operating-system or disk benchmark.
func BenchmarkDB022SQLite(b *testing.B) {
	ctx := context.Background()
	for _, volume := range []int{100, 1000, 10000, 100000} {
		b.Run(fmt.Sprint(volume), func(b *testing.B) {
			db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			all := qaPlaylist(volume)
			if err = db.SavePlaylist(ctx, "all", all); err != nil {
				b.Fatal(err)
			}
			for _, size := range []int{100, 1000} {
				if size <= volume {
					c := all
					c.Tracks = all.Tracks[:size]
					if err = db.SavePlaylist(ctx, fmt.Sprint(size), c); err != nil {
						b.Fatal(err)
					}
				}
			}

			inserted := qaPlaylist(100).Tracks
			for i := range inserted {
				inserted[i].SpotifyID = fmt.Sprintf("%022d", volume+i)
			}
			ops := []struct {
				name string
				run  func() error
			}{
				{"track_id", func() error {
					_, hit, e := db.LoadCatalog(ctx, all.Tracks[volume/2].SpotifyID)
					if e == nil && !hit {
						return fmt.Errorf("missing track")
					}
					return e
				}},
				{"playlist_id", func() error {
					var title string
					return db.db.QueryRowContext(ctx, "SELECT title FROM catalog_playlists WHERE spotify_id='all'").Scan(&title)
				}},
				{"position", func() error { _, e := db.CatalogPosition(ctx, "all", volume/2); return e }},
				{"full_playlist", func() error {
					c, hit, e := db.LoadPlaylist(ctx, "all")
					if e == nil && (!hit || len(c.Tracks) != volume) {
						return fmt.Errorf("incomplete snapshot")
					}
					return e
				}},
				{"batch_insert_100", func() error { return db.SaveCatalogBatch(ctx, inserted) }},
				{"metadata_update", func() error { return db.SaveCatalog(ctx, all.Tracks[volume/2]) }},
				{"batch_update_100", func() error { return db.SaveCatalogBatch(ctx, all.Tracks[:100]) }},
				{"remove_position", func() error {
					tx, e := db.db.BeginTx(ctx, nil)
					if e != nil {
						return e
					}
					defer tx.Rollback()
					if _, e = tx.ExecContext(ctx, "DELETE FROM catalog_positions WHERE playlist_id='all' AND position=?", volume/2); e != nil {
						return e
					}
					// Rollback restores the fixture; timing includes real delete and rollback.
					return tx.Rollback()
				}},
				{"normalized_artist_title", func() error {
					var id string
					return db.db.QueryRowContext(ctx, `SELECT spotify_id FROM music_catalog WHERE lower(json_extract(metadata,'$.Artist'))=lower(?) AND lower(json_extract(metadata,'$.Title'))=lower(?)`, all.Tracks[0].Artist, all.Tracks[0].Title).Scan(&id)
				}},
			}
			for _, size := range []int{100, 1000} {
				if size > volume {
					continue
				}
				ops = append(ops, struct {
					name string
					run  func() error
				}{fmt.Sprintf("playlist_%d", size), func() error {
					c, hit, e := db.LoadPlaylist(ctx, fmt.Sprint(size))
					if e == nil && (!hit || len(c.Tracks) != size) {
						return fmt.Errorf("incomplete playlist")
					}
					return e
				}})
			}
			if volume >= 1000 {
				ops = append(ops, struct {
					name string
					run  func() error
				}{"queue_1000", func() error {
					c, _, e := db.LoadPlaylist(ctx, "1000")
					if e != nil {
						return e
					}
					return qaQueue(ctx, c.Tracks)
				}})
			}
			for _, mode := range []string{"warm", "cold_sqlite"} {
				for _, op := range ops {
					b.Run(mode+"/"+op.name, func(b *testing.B) {
						samples := make([]float64, 0)
						b.ReportAllocs()
						b.ResetTimer()
						for b.Loop() {
							if op.name == "batch_insert_100" {
								b.StopTimer()
								if _, e := db.db.ExecContext(ctx, "DELETE FROM music_catalog WHERE spotify_id>=?", inserted[0].SpotifyID); e != nil {
									b.Fatal(e)
								}
								b.StartTimer()
							}

							if mode == "cold_sqlite" {
								b.StopTimer()
								if _, e := db.db.Exec("PRAGMA shrink_memory"); e != nil {
									b.Fatal(e)
								}
								b.StartTimer()
							}
							start := time.Now()
							e := op.run()
							elapsed := time.Since(start)
							if e != nil {
								b.Fatal(e)
							}
							samples = append(samples, float64(elapsed.Nanoseconds()))
						}
						b.StopTimer()
						slices.Sort(samples)
						for _, q := range []struct {
							name string
							p    float64
							min  int
						}{{"p50_ns", .5, 2}, {"p95_ns", .95, 20}, {"p99_ns", .99, 100}} {
							if len(samples) >= q.min {
								b.ReportMetric(samples[int(math.Ceil(q.p*float64(len(samples))))-1], q.name)
							}
						}
					})
				}
			}
		})
	}
}

// Run with TMPDIR on a verified persistent filesystem. Two independent SQLite
// handles and four writers contend on the actual WAL, with no network traffic.
func BenchmarkDB022DiskConcurrency(b *testing.B) {
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "writers.db")
	db, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	c := qaPlaylist(1000)
	if err = db.SavePlaylist(ctx, "concurrent", c); err != nil {
		b.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	samples := make([]float64, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for i := range 4 {
			wg.Go(func() {
				writer := db
				if i%2 == 1 {
					writer = other
				}
				track := c.Tracks[i]
				track.Title = "Concurrent durable update"
				errs <- writer.SaveCatalog(ctx, track)
			})
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				b.Fatal(e)
			}
		}
		samples = append(samples, float64(time.Since(start).Nanoseconds()))
	}
	b.StopTimer()
	slices.Sort(samples)
	for _, q := range []struct {
		name string
		p    float64
	}{{"p50_ns", .5}, {"p95_ns", .95}, {"p99_ns", .99}} {
		b.ReportMetric(samples[int(math.Ceil(q.p*float64(len(samples))))-1], q.name)
	}
	usage, err := db.PhysicalUsage(ctx)
	if err != nil || usage.Files > usage.Limit || usage.Peak > usage.Limit {
		b.Fatal(usage, err)
	}
	b.ReportMetric(float64(usage.Peak), "physical_peak_bytes")
	if err = other.Close(); err != nil {
		b.Fatal(err)
	}
	if err = db.Close(); err != nil {
		b.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	after, hit, err := db.LoadPlaylist(ctx, "concurrent")
	if err != nil || !hit || len(after.Tracks) != 1000 || after.Tracks[0].Title != "Concurrent durable update" {
		b.Fatal(hit, len(after.Tracks), err)
	}
	if err = qaQueue(ctx, after.Tracks); err != nil {
		b.Fatal(err)
	}
	var integrity string
	if err = db.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		b.Fatal(integrity, err)
	}
}
