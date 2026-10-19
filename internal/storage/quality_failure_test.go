package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestQualitySQLiteRollback(t *testing.T) {
	for _, table := range []string{"guilds", "history", "tracks"} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			// SaveGuild performs INSERT/UPDATE guilds, DELETE/INSERT history, and
			// INSERT/UPDATE tracks. Omit operations outside that transaction.
			if (table == "guilds" && operation == "DELETE") || (table == "history" && operation == "UPDATE") || (table == "tracks" && operation == "DELETE") {
				continue
			}
			t.Run(table+"/"+operation, func(t *testing.T) {
				db := openTest(t)
				ctx := context.Background()
				old := player.DurableState{Loop: "queue", Autoplay: true, Queue: []media.Track{{Title: "original", Artists: []string{"artist"}}}, History: []media.Track{{Title: "past"}}}
				if err := db.SaveGuild(ctx, "1", old); err != nil {
					t.Fatal(err)
				}
				if err := db.SaveGuild(ctx, "2", old); err != nil {
					t.Fatal(err)
				}
				// All identifiers come from the fixed allowlist above.
				_, err := db.db.Exec(fmt.Sprintf("CREATE TRIGGER injected BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'quality failure'); END", operation, table))
				if err != nil {
					t.Fatal(err)
				}
				next := old
				next.Loop = "off"
				next.History = []media.Track{{Title: "replacement"}}
				// Preserve the same track to exercise ON CONFLICT UPDATE as well.
				if err = db.SaveGuild(ctx, "1", next); err == nil {
					t.Fatal("injected write failure ignored")
				}
				for _, id := range []string{"1", "2"} {
					got, err := db.LoadGuild(ctx, id)
					if err != nil || !reflect.DeepEqual(got, old) {
						t.Fatalf("guild %s partially committed: %+v %v", id, got, err)
					}
				}
				var rows int
				if err = db.db.QueryRow("SELECT count(*) FROM history").Scan(&rows); err != nil || rows != 2 {
					t.Fatalf("history=%d err=%v", rows, err)
				}
			})
		}
	}
}
func TestQualitySQLiteRestart(t *testing.T) {
	for _, size := range []int{0, 1, 500, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recovery.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			state := player.DurableState{Loop: "track", Autoplay: true, Current: &media.Track{Title: "current", Explicit: new(true)}, History: []media.Track{{Title: "previous"}}}
			for i := range size {
				state.Queue = append(state.Queue, media.Track{Title: fmt.Sprint(i), SpotifyID: fmt.Sprint(i), Artists: []string{"artist"}, Position: i + 1, Total: size})
			}
			if err = db.SaveGuild(ctx, "1", state); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err = db.RecordPlayback(ctx, "1", *state.Current); err != nil {
				db.Close()
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
			got, err := db.LoadGuild(ctx, "1")
			if err != nil || !reflect.DeepEqual(got, state) {
				t.Fatalf("restart lost state: %v", err)
			}
			events, err := db.PlaybackHistory(ctx, "1", 1000)
			if err != nil || len(events) != 1 || !reflect.DeepEqual(events[0], *state.Current) {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			if err := db.db.QueryRow("PRAGMA integrity_check").Scan(&path); err != nil || path != "ok" {
				t.Fatal(path, err)
			}
		})
	}
}
func TestQualitySQLiteUnavailable(t *testing.T) {
	for _, mode := range []string{"closed", "canceled", "readonly"} {
		t.Run(mode, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			if err := db.SaveGuild(ctx, "1", player.DurableState{}); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "closed":
				db.Close()
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "readonly":
				if _, err := db.db.Exec("PRAGMA query_only=ON"); err != nil {
					t.Fatal(err)
				}
			}
			for name, write := range map[string]func() error{
				"guild":      func() error { return db.SaveGuild(ctx, "1", player.DurableState{}) },
				"settings":   func() error { return db.SaveSettings(ctx, "1", map[string]string{"a": "b"}) },
				"mapping":    func() error { return db.SaveMapping(ctx, "key", media.Candidate{ID: "abcdefghijk"}) },
				"invalidate": func() error { return db.DeleteMapping(ctx, "key") },
				"event":      func() error { return db.RecordPlayback(ctx, "1", media.Track{Title: "track"}) },
			} {
				t.Run(name, func(t *testing.T) {
					if err := write(); err == nil {
						t.Fatal("write failure hidden")
					}
				})
			}
			if mode != "readonly" {
				if _, err := db.LoadGuild(ctx, "1"); err == nil {
					t.Fatal("read failure hidden")
				}
				if _, _, err := db.LoadMapping(ctx, "key"); err == nil {
					t.Fatal("mapping failure hidden")
				}
				if _, err := db.PlaybackHistory(ctx, "1", 10); err == nil {
					t.Fatal("history failure hidden")
				}
			}
		})
	}
}
func TestQualitySQLiteCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is deliberately not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err == nil {
		db.Close()
		t.Fatal("corrupt database opened")
	}
}
func TestQualitySQLiteConcurrentGuilds(t *testing.T) {
	db := openTest(t)
	gate := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for user := range 20 {
		wg.Go(func() {
			<-gate
			id := fmt.Sprint(user)
			for i := range 10 {
				state := player.DurableState{Queue: []media.Track{{Title: fmt.Sprintf("%s/%d", id, i)}}}
				if err := db.SaveGuild(context.Background(), id, state); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for user := range 20 {
		got, err := db.LoadGuild(context.Background(), fmt.Sprint(user))
		if err != nil || len(got.Queue) != 1 || got.Queue[0].Title != fmt.Sprintf("%d/9", user) {
			t.Fatal(user, got, err)
		}
	}
}
