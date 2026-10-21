package storage

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestREL024SQLiteBusyCheckpointRecoversWithoutNewCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	blocker, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	ctx := context.Background()
	entered := make(chan struct{})
	p := player.New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, _ player.Voice) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.Close(ctx)
	if err = p.ConfigureStore(db, "1"); err != nil {
		t.Fatal(err)
	}
	if err = p.Attach(voice{}); err != nil {
		t.Fatal(err)
	}
	if err = p.Enqueue(media.Track{Title: "held", Position: 1}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("player did not start")
	}
	tx, err := blocker.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE guilds SET state=state"); err != nil {
		t.Fatal(err)
	}
	// The SQLite write lock is real. No mock store chooses the failure outcome.
	started := time.Now()
	if err = p.EnqueueMany([]media.Track{{Title: "duplicate", Position: 2}, {Title: "duplicate", Position: 3}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		saved, e := db.LoadGuild(ctx, "1")
		n := len(saved.Queue)
		if saved.Current != nil {
			n++
		}
		if e == nil && n == 3 && p.State().PersistenceError == "" {
			if e = db.CheckHealth(ctx); e != nil {
				t.Fatal(e)
			}
			t.Logf("real SQLITE_BUSY: checkpoint automatically recovered in %s; positions=3/3", time.Since(started))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("checkpoint not retried after SQLite lock recovered")
}
