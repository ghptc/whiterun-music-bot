package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"whiterun/internal/discord"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func TestREL024ProcessFixture(t *testing.T) {
	path := os.Getenv("REL024_FIXTURE_DB")
	if path == "" {
		t.Skip("isolated child only")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// An unavailable log store must fall back without disabling playback/state.
	log, logs := persistentLogger(log)
	if logs != nil {
		t.Fatal("fixture requires failed log persistence")
	}
	p := player.New(ctx, log, func(context.Context, media.Track, player.Voice) error {
		panic("must not play without authorized voice")
	})
	defer p.Close(context.Background())
	tracks := make([]media.Track, 123)
	for i := range tracks {
		tracks[i] = media.Track{SpotifyID: fmt.Sprint(i % 7), Title: "Unicode 音楽", Position: i + 1, Total: 123}
	}
	saved, err := db.LoadGuild(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Current == nil && len(saved.Queue) == 0 {
		if err = db.SaveGuild(ctx, "1", player.DurableState{Current: &tracks[0], Queue: tracks[1:]}); err != nil {
			t.Fatal(err)
		}
	}
	if err = p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	state := p.State()
	if state.Current != nil || len(state.Entries) != 123 {
		t.Fatal("fixture restore", len(state.Entries))
	}
	for i, e := range state.Entries {
		if e.Track.Position != i+1 || e.Track.SpotifyID != fmt.Sprint(i%7) {
			t.Fatal("fixture order", i)
		}
	}
	fmt.Println("REL024_READY positions=123/123 voice=detached")
	if os.Getenv("REL024_FIXTURE_MODE") == "watchdog" {
		go func() {
			poll := time.NewTicker(100 * time.Millisecond)
			defer poll.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-poll.C:
					if _, err := os.Stat(path + ".inject-panic"); err == nil {
						_ = os.Remove(path + ".inject-panic")
						panic("REL024 isolated fatal worker panic")
					}
				}
			}
		}()
		marker := path + ".injected-stall"
		if _, e := os.Stat(marker); os.IsNotExist(e) {
			if e = os.WriteFile(marker, []byte("one isolated stall"), 0600); e != nil {
				t.Fatal(e)
			}
			started := time.Now()
			go monitorRuntime(ctx, slog.New(slog.NewJSONHandler(os.Stdout, nil)), func(context.Context) runtimeHealth {
				<-ctx.Done()
				return runtimeHealth{Runtime: discord.Health{}}
			}, func() {
				fmt.Printf("REL024_WATCHDOG_RESTART detection_ms=%d\n", time.Since(started).Milliseconds())
				os.Exit(1)
			}, path+".supervision.json")
		}
	}
	if os.Getenv("REL024_FIXTURE_MODE") == "verify" {
		return
	}
	if os.Getenv("REL024_FIXTURE_MODE") == "panic" {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		panic("isolated fatal worker panic")
	}
	<-ctx.Done()
}
func TestREL024ProcessFaultsPreserve123Positions(t *testing.T) {
	for _, mode := range []string{"SIGTERM", "SIGKILL", "panic"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "music.db")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestREL024ProcessFixture$")
			cmd.Env = append(os.Environ(), "REL024_FIXTURE_DB="+path, "REL024_FIXTURE_MODE="+mode, "BARD_LOG_DB_PATH="+t.TempDir())
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			scanner := bufio.NewScanner(stdout)
			ready := false
			for scanner.Scan() {
				t.Log(scanner.Text())
				if strings.HasPrefix(scanner.Text(), "REL024_READY") {
					ready = true
					break
				}
			}
			if !ready {
				t.Fatal("fixture not ready", scanner.Err())
			}
			started := time.Now()
			switch mode {
			case "SIGTERM":
				err = cmd.Process.Signal(syscall.SIGTERM)
			case "SIGKILL":
				err = cmd.Process.Kill()
			case "panic":
				_, err = io.WriteString(stdin, "panic\n")
			}
			if err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait()
			if mode == "SIGTERM" && waitErr != nil {
				t.Fatal(waitErr)
			}
			if mode != "SIGTERM" && waitErr == nil {
				t.Fatal("crash fixture exited successfully")
			}
			db, err := storage.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err = db.CheckHealth(ctx); err != nil {
				t.Fatal(err)
			}
			p := player.New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(context.Context, media.Track, player.Voice) error {
				t.Error("unauthorized automatic playback")
				return nil
			})
			defer p.Close(context.Background())
			if err = p.ConfigureStore(db, "1"); err != nil {
				t.Fatal(err)
			}
			state := p.State()
			if state.Current != nil || len(state.Entries) != 123 {
				t.Fatal("positions lost", len(state.Entries))
			}
			for i, e := range state.Entries {
				if e.Track.Position != i+1 || e.Track.SpotifyID != fmt.Sprint(i%7) {
					t.Fatal(i, e)
				}
			}
			elapsed := time.Since(started)
			if elapsed > 30*time.Second {
				t.Fatal(elapsed)
			}
			t.Logf("%s: restart/read=%s; positions=123/123; no voice attached; failed logs tolerated", mode, elapsed)
		})
	}
}
