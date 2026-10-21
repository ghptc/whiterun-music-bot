package storage

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDB022CrashBeforeCommitHelper(t *testing.T) {
	path := os.Getenv("DB022_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess only")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if os.Getenv("DB022_CRASH_COMMIT") == "1" {
		track := catalogTrack()
		track.SpotifyID = "committed-before-crash"
		track.Title = "durable WAL"
		if err = db.SaveCatalog(context.Background(), track); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE catalog_playlists SET title='interrupted import' WHERE spotify_id='crash'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("DELETE FROM catalog_positions WHERE playlist_id='crash'"); err != nil {
		t.Fatal(err)
	}
	fmt.Println("DB022_UNCOMMITTED")
	// The parent kills this process with the real journal transaction still open.
	for {
		time.Sleep(time.Second)
	}
}
func TestDB022ProcessDeathDuringImport(t *testing.T)    { db022ProcessDeath(t, false) }
func TestDB022PhysicalCrashWithPendingWAL(t *testing.T) { db022ProcessDeath(t, true) }
func db022ProcessDeath(t *testing.T, physical bool) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crash.db")
	var db *DB
	var err error
	if physical {
		db, err = OpenWithPhysicalQuota(path, 2<<20)
	} else {
		db, err = Open(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	original := qaPlaylist(100)
	if err = db.SavePlaylist(ctx, "crash", original); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	childCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestDB022CrashBeforeCommitHelper$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "DB022_CRASH_PATH="+path)
	if physical {
		cmd.Env = append(cmd.Env, "DB022_CRASH_COMMIT=1")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "DB022_UNCOMMITTED" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("child failed before write", scanner.Err())
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if physical {
		info, e := os.Stat(path + "-wal")
		if e != nil || info.Size() == 0 {
			t.Fatal("fixture must retain real WAL", e)
		}
		if bytes := physicalFiles(t, path); bytes > 2<<20 {
			t.Fatal("crash overshoot", bytes)
		}
		t.Logf("SIGKILL retained WAL=%d bytes", info.Size())
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, hit, err := db.LoadPlaylist(ctx, "crash")
	if err != nil || !hit || len(c.Tracks) != 100 || c.Title != original.Title {
		t.Fatal(len(c.Tracks), c.Title, hit, err)
	}
	u, err := db.CatalogUsage(ctx)
	if err != nil || u.Used > u.Limit {
		t.Fatal(u, err)
	}
	if physical {
		track, hit, e := db.LoadCatalog(ctx, "committed-before-crash")
		if e != nil || !hit || track.Title != "durable WAL" {
			t.Fatal("committed WAL lost", hit, e)
		}
		u, e := db.PhysicalUsage(ctx)
		if e != nil || u.Files > u.Limit {
			t.Fatal(u, e)
		}
	}
	assertCatalogIntegrity(t, db)
}
