package discord

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func TestQA013CommandPersistenceRollbackAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	injector, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer injector.Close()
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "playing", db)
		old, err := db.LoadGuild(context.Background(), "1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := injector.Exec("CREATE TRIGGER qa013_abort BEFORE UPDATE ON guilds BEGIN SELECT RAISE(ABORT,'qa013 injected write failure'); END"); err != nil {
			t.Fatal(err)
		}
		e := f.interaction("move")
		e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
		f.b.command(e)
		synctest.Wait()
		state := f.g.player.State()
		if state.PersistenceError == "" || len(state.Queued) != 3 || state.Queued[0].Title != "c" {
			t.Fatal("persistence fault did not preserve in-memory command effect", state)
		}
		saved, err := db.LoadGuild(context.Background(), "1")
		if err != nil || !reflect.DeepEqual(saved, old) {
			t.Fatal("failed transaction partially committed", saved, err)
		}
		if _, err := injector.Exec("DROP TRIGGER qa013_abort"); err != nil {
			t.Fatal(err)
		}
		f.model.command(f, "move")
		f.eof()
		if f.g.player.State().PersistenceError != "" {
			t.Fatal("successful transition did not clear persistence warning")
		}
		f.request("play")
		f.eof()
		f.request("stop")
		f.g.player.Close(context.Background())
	})
}

type lifecycleFailAnnouncement struct {
	rest.Rest
	calls atomic.Int64
}

func (r *lifecycleFailAnnouncement) CreateMessage(id snowflake.ID, m d.MessageCreate, opts ...rest.RequestOpt) (*d.Message, error) {
	if r.calls.Add(1) == 1 {
		return nil, errors.New("injected announcement HTTP failure")
	}
	return r.Rest.CreateMessage(id, m, opts...)
}
func TestQA013AnnouncementFailureDoesNotBlockNextTrack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "idle", nil)
		restFault := &lifecycleFailAnnouncement{Rest: f.b.client.Rest}
		f.b.client.Rest = restFault
		f.g.announcementChannel.Store(9)
		f.request("play")
		f.request("play")
		f.eof()
		if restFault.calls.Load() != 2 {
			t.Fatal("announcement failure suppressed successor or retried first", restFault.calls.Load())
		}
		select {
		case message := <-f.r.messages:
			if message.channel != 9 {
				t.Fatal("wrong announcement destination")
			}
		default:
			t.Fatal("successor announcement missing")
		}
		f.request("stop")
	})
}
func TestQA013ConcurrentGuildIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "playing", nil)
		g := f.b.getGuild(2)
		g.player.Close(context.Background())
		g.player = player.New(f.b.ctx, f.b.log, func(ctx context.Context, _ media.Track, v player.Voice) error {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err := g.player.ConfigureStore(f.store, "2"); err != nil {
			t.Fatal(err)
		}
		if err := g.player.Attach(qualityVoice{}); err != nil {
			t.Fatal(err)
		}
		g.conn = &lifecycleConn{}
		g.channel = 5
		listener(f.b, 2, 4, false)
		first := f.interaction("play")
		second := f.interactionFor("play", 2, 4)
		second.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
		first.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
		gate := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-gate; f.b.command(first) }()
		go func() { defer wg.Done(); <-gate; f.b.command(second) }()
		close(gate)
		wg.Wait()
		synctest.Wait()
		f.model.queue = append(f.model.queue, "request")
		f.check("parallel guild play")
		before := g.player.State()
		if before.Current == nil || before.Current.RequestedBy != "4" || g.player.Length() != 0 {
			t.Fatal("guild 2 request crossed guild boundary", before)
		}
		ticket := g.player.Ticket()
		f.request("stop")
		if !reflect.DeepEqual(before, g.player.State()) || ticket != g.player.Ticket() {
			t.Fatal("guild 1 stop affected guild 2")
		}
		saved, err := f.store.LoadGuild(context.Background(), "2")
		if err != nil || saved.Current == nil || saved.Current.RequestedBy != "4" {
			t.Fatal("guild 2 durable identity lost", err)
		}
		g.player.Stop()
		synctest.Wait()
		g.player.Close(context.Background())
	})
}
