package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

// Abrupt subprocess exit after outbound frames, without player/SQLite Close.
// This exercises the actual WAL/checkpoint path, not an in-memory mock store.
func TestFIX025CrashCheckpointHelper(t *testing.T) {
	path := os.Getenv("BARD_FIX025_CRASH_DB")
	if path == "" {
		t.Skip("subprocess-only crash fixture")
	}
	frames, err := strconv.Atoi(os.Getenv("BARD_FIX025_CRASH_FRAMES"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := player.New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v player.Voice) error {
		for range frames {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
		}
		os.Exit(19)
		return nil
	})
	if err = p.ConfigureStore(db, "crash-guild"); err != nil {
		t.Fatal(err)
	}
	if err = p.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	if err = p.EnqueueMany([]media.Track{{Title: "current", Position: 1, Total: 2, Duration: 100 * time.Second}, {Title: "next", Position: 2, Total: 2}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestFIX025RealCrashRestoresCommittedProgressOnce(t *testing.T) {
	for _, tc := range []struct{ frames, seconds, positions int }{{600, 10, 2}, {3100, 60, 1}} {
		t.Run(strconv.Itoa(tc.seconds), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFIX025CrashCheckpointHelper$")
			cmd.Env = append(os.Environ(), "BARD_FIX025_CRASH_DB="+path, "BARD_FIX025_CRASH_FRAMES="+strconv.Itoa(tc.frames))
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 19 {
				t.Fatalf("crash fixture didn't exit as intended: %v %s", err, out)
			}
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			saved, err := db.LoadGuild(ctx, "crash-guild")
			if err != nil || saved.Current == nil || saved.Current.ResumeOffset != time.Duration(tc.seconds)*time.Second || len(saved.Queue) != 1 {
				t.Fatalf("committed checkpoint lost: %+v %v", saved, err)
			}
			p := player.New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, media.Track, player.Voice) error {
				t.Error("played without explicit Voice attachment")
				return nil
			})
			defer p.Close(context.Background())
			if err = p.ConfigureStore(db, "crash-guild"); err != nil {
				t.Fatal(err)
			}
			state := p.State()
			if state.Current != nil || len(state.Queued) != tc.positions {
				t.Fatal("restore duplicated/consumed positions", state)
			}
			if tc.positions == 2 && state.Queued[0].ResumeOffset != 10*time.Second {
				t.Fatal("saved seek lost")
			}
			if tc.positions == 1 && state.Queued[0].Title != "next" {
				t.Fatal("advanced twice")
			}
			ledger, err := db.PlaybackHistory(ctx, "crash-guild", 10)
			if err != nil || len(ledger) != 1 {
				t.Fatal("crash duplicated playback ledger", ledger, err)
			}
		})
	}
}
