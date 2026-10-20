package discord

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func TestQA013EveryCommandSQLiteRestart(t *testing.T) {
	for _, command := range commands() {
		t.Run(command.CommandName(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lifecycle.db")
			db, err := storage.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.SaveGuild(context.Background(), "2", player.DurableState{Queue: []media.Track{{Title: "other guild"}}}); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, "playing", db)
				f.request(command.CommandName())
				f.eof()
				f.request("history")
				before, err := db.LoadGuild(context.Background(), "1")
				if err != nil {
					t.Fatal(err)
				}
				ledger, err := db.PlaybackHistory(context.Background(), "1", 100)
				if err != nil || len(ledger) == 0 {
					t.Fatal("played ledger missing", err)
				}
				for _, tr := range append(append([]media.Track{}, before.Queue...), before.History...) {
					if tr.SpotifyArtworkURL == "" {
						t.Fatal("artwork dropped", tr.Title)
					}
				}
				f.g.player.Close(context.Background())
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := storage.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				f.store = reopened
				f.b.store = reopened
				recovered := player.New(f.b.ctx, f.b.log, f.play)
				if err := recovered.ConfigureStore(reopened, "1"); err != nil {
					t.Fatal(err)
				}
				pending := lifecycleTitles(before.Queue)
				if before.Current != nil {
					pending = append([]string{before.Current.Title}, pending...)
				}
				synctest.Wait()
				if s := recovered.State(); s.Current != nil || !slices.Equal(lifecycleTitles(s.Queued), pending) {
					t.Fatal("recovery autoplayed or reordered", s)
				}
				// Rejoin through the real handler, then exercise another natural EOF.
				history := lifecycleTitles(before.History)
				if before.CurrentStarted && before.Current != nil && len(history) > 0 {
					history = history[:len(history)-1]
				}
				f.g.player = recovered
				f.g.conn = nil
				f.g.channel = 0
				f.model = lifecycleOracle{queue: pending, history: history, loop: before.Loop, autoplay: before.Autoplay}
				f.b.bindAnnouncements(f.g)
				f.request("summon")
				f.request("history")
				if !strings.Contains(f.r.response, ledger[0].Title) {
					t.Fatal("history lost across command/restart", f.r.response)
				}
				other, err := reopened.LoadGuild(context.Background(), "2")
				if err != nil || len(other.Queue) != 1 || other.Queue[0].Title != "other guild" {
					t.Fatal("guild isolation failed", other, err)
				}
				f.eof()
				f.request("stop")
				recovered.Close(context.Background())
			})
		})
	}
}
