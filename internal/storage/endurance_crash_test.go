//go:build stress

package storage

import (
	"bufio"
	"context"
	"database/sql/driver"
	"fmt"
	"modernc.org/sqlite"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestEnduranceCrashWriterHelper(t *testing.T) {
	path := os.Getenv("QA010_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper only")
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(path), "qa-owned"))
	if err != nil || string(marker) != "qa010" {
		t.Fatal("owned temporary database required")
	}
	if err := sqlite.RegisterScalarFunction("qa010_crash_gate", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		fmt.Println("QA010_WRITE_ENTERED")
		select {}
	}); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stage := os.Getenv("QA010_CRASH_STAGE")
	allow := map[string]string{"guild": "AFTER UPDATE ON guilds", "history_delete": "AFTER DELETE ON history", "history_insert": "AFTER INSERT ON history", "catalog": "AFTER INSERT ON tracks"}
	target, ok := allow[stage]
	if !ok {
		t.Fatal("unknown stage")
	}
	if _, err = db.db.Exec("CREATE TRIGGER qa010_interruption " + target + " BEGIN SELECT qa010_crash_gate(); END"); err != nil {
		t.Fatal(err)
	}
	err = db.SaveGuild(context.Background(), "one", player.DurableState{Loop: "off", Queue: []media.Track{{Title: "new"}}, History: []media.Track{{Title: "new past"}}})
	t.Fatalf("writer escaped crash gate: %v", err)
}
func TestEnduranceKillDuringSQLiteWriteRecoversCommittedState(t *testing.T) {
	for _, stage := range []string{"guild", "history_delete", "history_insert", "catalog"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "qa.db")
			if err := os.WriteFile(filepath.Join(dir, "qa-owned"), []byte("qa010"), 0600); err != nil {
				t.Fatal(err)
			}
			old := player.DurableState{Loop: "queue", Current: &media.Track{Title: "active", SpotifyID: "identity", SpotifyArtworkURL: "https://qa.invalid/art"}, Queue: []media.Track{{Title: "pending"}}, History: []media.Track{{Title: "past"}}}
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, guild := range []string{"one", "two"} {
				if err = db.SaveGuild(context.Background(), guild, old); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.SaveSettings(context.Background(), "one", map[string]string{"announcement_channel": "99"}); err != nil {
				t.Fatal(err)
			}
			candidate := media.Candidate{ID: "abcdefghijk", Title: "recording"}
			if err = db.SaveMapping(context.Background(), "identity", candidate); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEnduranceCrashWriterHelper$", "-test.timeout=15s")
			cmd.Env = []string{"QA010_CRASH_DB=" + path, "QA010_CRASH_STAGE=" + stage, "GORACE=atexit_sleep_ms=0"}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := false
			defer func() {
				if !done {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			entered := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if scan.Text() == "QA010_WRITE_ENTERED" {
						entered <- true
						return
					}
				}
				entered <- false
			}()
			select {
			case ok := <-entered:
				if !ok {
					t.Fatal("writer never entered transaction")
				}
			case <-ctx.Done():
				t.Fatal("writer gate timed out")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("killed writer reported success")
			}
			done = true
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, guild := range []string{"one", "two"} {
				got, err := db.LoadGuild(context.Background(), guild)
				if err != nil || !reflect.DeepEqual(got, old) {
					t.Fatalf("crash corrupted guild %s: %+v %v", guild, got, err)
				}
			}
			settings, err := db.LoadSettings(context.Background(), "one")
			if err != nil || settings["announcement_channel"] != "99" {
				t.Fatal(settings, err)
			}
			got, hit, err := db.LoadMapping(context.Background(), "identity")
			if err != nil || !hit || !reflect.DeepEqual(got, candidate) {
				t.Fatal(got, hit, err)
			}
			var integrity string
			if err = db.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatal(integrity, err)
			}
		})
	}
}
