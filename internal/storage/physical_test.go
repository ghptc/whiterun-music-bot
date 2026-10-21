package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"whiterun/internal/logstore"
	"whiterun/internal/media"
)

func physicalFiles(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".quota", ".quota-lock"} {
		fi, e := os.Stat(path + suffix)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		n := rounded(fi.Size())
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			n = max(n, st.Blocks*512)
		}
		total += n
	}
	return total
}
func TestDB022PhysicalQuotaWALRecoveryAndConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "physical.db")
	const limit = 1 << 20
	db, err := OpenWithPhysicalQuota(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	original := qaPlaylist(10)
	if err = db.SavePlaylist(ctx, "kept", original); err != nil {
		t.Fatal(err)
	}
	other, err := OpenWithPhysicalQuota(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	// Pin a real WAL reader so checkpoints cannot reclaim frames.
	read, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var title string
	if err = read.QueryRow("SELECT title FROM catalog_playlists WHERE spotify_id='kept'").Scan(&title); err != nil {
		t.Fatal(err)
	}
	failed := false
	peak := int64(0)
	for i := range 200 {
		track := catalogTrack()
		track.SpotifyID = fmt.Sprintf("%022d", i+100)
		track.Title = strings.Repeat("界", 1024)
		e := other.SaveCatalog(ctx, track)
		bytes := physicalFiles(t, path)
		peak = max(peak, bytes)
		if bytes > limit {
			t.Fatalf("physical overshoot: %d>%d", bytes, limit)
		}
		if e != nil {
			if !strings.Contains(e.Error(), "full") {
				t.Fatal(e)
			}
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("fixture failed to reach physical capacity")
	}
	// Preserved snapshot remains readable while growth is refused.
	if err = read.Rollback(); err != nil {
		t.Fatal(err)
	}
	c, hit, err := other.LoadPlaylist(ctx, "kept")
	if err != nil || !hit || len(c.Tracks) != 10 {
		t.Fatal(len(c.Tracks), hit, err)
	}
	if err = qaQueue(ctx, c.Tracks); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			track := catalogTrack()
			track.Title = strings.Repeat("x", 64<<10)
			track.SpotifyID = fmt.Sprintf("%022d", i+900)
			_ = other.SaveCatalog(ctx, track)
			if n := physicalFiles(t, path); n > limit {
				t.Errorf("concurrent overshoot %d", n)
			}
		})
	}
	wg.Wait()
	if err = other.Close(); err != nil {
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
	c, hit, err = db.LoadPlaylist(ctx, "kept")
	if err != nil || !hit || len(c.Tracks) != 10 {
		t.Fatal("restart", len(c.Tracks), hit, err)
	}
	assertCatalogIntegrity(t, db)
	var integrity string
	if err = db.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	usage, err := db.PhysicalUsage(ctx)
	if err != nil || usage.Files > limit || usage.Peak > limit {
		t.Fatal(usage, err)
	}
	t.Logf("limit=%d peak_files=%d restart_usage=%+v; queue=10 integrity=ok", limit, peak, usage)
}
func TestDB022PhysicalQuotaRejectsAtomicOversizedBatch(t *testing.T) {
	db, err := OpenWithPhysicalQuota(filepath.Join(t.TempDir(), "batch.db"), 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c := qaPlaylist(4)
	if err = db.SavePlaylist(ctx, "kept", c); err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.Title = "must not commit"
	changed.Tracks = append([]media.Track(nil), c.Tracks...)
	changed.Tracks[2].Title = strings.Repeat("x", 2<<20)
	if err = db.SavePlaylist(ctx, "kept", changed); err == nil {
		t.Fatal("oversized physical transaction committed")
	}
	got, hit, err := db.LoadPlaylist(ctx, "kept")
	if err != nil || !hit || got.Title != c.Title || got.Tracks[2].Title != c.Tracks[2].Title {
		t.Fatal(got.Title, hit, err)
	}
	u, err := db.PhysicalUsage(ctx)
	if err != nil || u.Files > u.Limit {
		t.Fatal(u, err)
	}
	if e := db.physical.locked(func() error { return db.physical.load() }); e != nil {
		t.Fatal(e)
	}
	if !db.physical.saved.Full.Pending {
		t.Fatal("physical rejection did not persist critical alert")
	}

	// The 500 MiB log budget is owned by another SQLite/VFS and remains writable
	// while music allocations are rejected.
	if logstore.DefaultMaxBytes != 524288000 {
		t.Fatal("log default changed")
	}
	logs, e := logstore.Open(logstore.Options{Path: filepath.Join(t.TempDir(), "logs.db"), OperationalPath: db.physical.path})
	if e != nil {
		t.Fatal(e)
	}
	logger := slog.New(logstore.NewHandler(slog.NewJSONHandler(io.Discard, nil), logs))
	logger.Info("music_quota_reached")
	if e = logs.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if metrics := logs.Metrics(); metrics.Written != 1 || metrics.Failures != 0 {
		t.Fatal(metrics)
	}
	after, e := db.PhysicalUsage(ctx)
	if e != nil || after.Files != u.Files {
		t.Fatal("logs consumed music budget", u, after, e)
	}
	t.Logf("oversized batch rolled back; files=%d limit=%d critical persisted", u.Files, u.Limit)
}

func TestDB022PhysicalConcurrentWriterHelper(t *testing.T) {
	path := os.Getenv("DB022_PHYSICAL_WRITER")
	if path == "" {
		t.Skip("subprocess only")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range 16 {
		track := catalogTrack()
		track.SpotifyID = fmt.Sprintf("%s-%022d", os.Getenv("DB022_PHYSICAL_SEED"), i)
		track.Title = strings.Repeat("x", 32<<10)
		e := db.SaveCatalog(context.Background(), track)
		u, err := db.PhysicalUsage(context.Background())
		if err != nil || u.Files > u.Limit || u.Peak > u.Limit {
			t.Fatal(u, err)
		}
		if e != nil {
			if !strings.Contains(e.Error(), "full") {
				t.Fatal(e)
			}
			break
		}
	}
}
func TestDB022PhysicalQuotaAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.db")
	const limit = 2 << 20
	db, err := OpenWithPhysicalQuota(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SavePlaylist(context.Background(), "kept", qaPlaylist(10)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var commands []*exec.Cmd
	for i := range 3 {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDB022PhysicalConcurrentWriterHelper$", "-test.timeout=25s")
		cmd.Env = append(os.Environ(), "DB022_PHYSICAL_WRITER="+path, fmt.Sprintf("DB022_PHYSICAL_SEED=%d", i))
		commands = append(commands, cmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	for _, cmd := range commands {
		if err = cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.PhysicalUsage(context.Background())
	if err != nil || u.Files > limit || u.Peak > limit {
		t.Fatal(u, err)
	}
	c, hit, err := db.LoadPlaylist(context.Background(), "kept")
	if err != nil || !hit || len(c.Tracks) != 10 {
		t.Fatal(len(c.Tracks), hit, err)
	}
	assertCatalogIntegrity(t, db)
	t.Logf("three real writer processes; limit=%d final=%d queue=10", limit, u.Files)
}

func TestDB022PhysicalQuotaTornControlRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.db")
	db, err := OpenWithPhysicalQuota(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = db.SavePlaylist(ctx, "kept", qaPlaylist(10)); err != nil {
		t.Fatal(err)
	}
	// Tear the newest fixed record in an isolated file, leaving the previous
	// durable record intact. The cap must survive; guessing a larger cap is unsafe.
	if err = db.physical.locked(func() error {
		q := db.physical
		if e := q.load(); e != nil {
			return e
		}
		offset := int64(q.saved.Sequence%2)*physicalSlot + 16
		if _, e := q.state.WriteAt(make([]byte, 64), offset); e != nil {
			return e
		}
		return q.state.Sync()
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, e := OpenWithPhysicalQuota(path, DefaultPhysicalQuota+1); e == nil {
		t.Fatal("hard 10 GiB ceiling bypassed")
	}
	if _, e := OpenWithPhysicalQuota(path, 2<<20); e == nil {
		t.Fatal("persistent reduced limit silently increased")
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.PhysicalUsage(ctx)
	if err != nil || u.Limit != 1<<20 || u.Files > u.Limit {
		t.Fatal(u, err)
	}
	c, hit, err := db.LoadPlaylist(ctx, "kept")
	if err != nil || !hit || len(c.Tracks) != 10 {
		t.Fatal(len(c.Tracks), hit, err)
	}
	t.Log("torn newest quota record recovered; configured cap and 10 positions preserved")
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(path+".quota", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path); err == nil {
		t.Fatal("empty existing control state silently reset to a larger quota")
	}
}
