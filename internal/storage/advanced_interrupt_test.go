package storage

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

var advancedFunctionSerial atomic.Uint64

func TestAdvancedInterruptedSQLiteTransaction(t *testing.T) {
	for _, stage := range []struct{ name, event, table string }{
		{"guild replacement", "UPDATE", "guilds"},
		{"history deletion", "DELETE", "history"},
		{"history insertion", "INSERT", "history"},
		{"catalog insert", "INSERT", "tracks"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			// Register before opening this connection. Names are unique even under
			// -count; no global hook is replaced while another test could use it.
			entered, release := make(chan struct{}), make(chan struct{})
			name := fmt.Sprintf("qa008_gate_%d", advancedFunctionSerial.Add(1))
			if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
				select {
				case <-entered:
				default:
					close(entered)
				}
				<-release
				return int64(0), nil
			}); err != nil {
				t.Fatal(err)
			}
			db := openTest(t)
			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			old := player.DurableState{Loop: "queue", Queue: []media.Track{{Title: "original"}}, History: []media.Track{{Title: "past"}}}
			if err := db.SaveGuild(ctx, "1", old); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveGuild(ctx, "2", old); err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf("CREATE TRIGGER interruption AFTER %s ON %s BEGIN SELECT %s(); END", stage.event, stage.table, name)
			if _, err := db.db.ExecContext(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- db.SaveGuild(bounded, "1", player.DurableState{Loop: "off", Queue: []media.Track{{Title: "new"}}, History: []media.Track{{Title: "new past"}}})
			}()
			// Real driver and SQL transaction; cancellation occurs after a write has
			// entered the transaction, rather than before SaveGuild is called.
			select {
			case <-entered:
			case <-bounded.Done():
				close(release)
				t.Fatal("write did not reach injection point")
			}
			cancel()
			close(release)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted transaction committed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interrupted write leaked")
			}
			for _, guild := range []string{"1", "2"} {
				got, err := db.LoadGuild(ctx, guild)
				if err != nil || !reflect.DeepEqual(got, old) {
					t.Fatalf("guild=%s partial commit %+v err=%v", guild, got, err)
				}
			}
		})
	}
}
