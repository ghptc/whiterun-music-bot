package discord

import (
	"context"
	"github.com/disgoorg/disgo/gateway"
	"testing"
	"testing/synctest"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func idleGuild(t *testing.T, b *Bot, id snowflake.ID) (*guild, *dummyConn) {
	t.Helper()
	g := b.getGuild(id)
	g.player.Close(context.Background())
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, _ player.Voice) error { <-ctx.Done(); return ctx.Err() })
	conn := &dummyConn{}
	if err := g.player.Attach(transport{conn: conn}); err != nil {
		t.Fatal(err)
	}
	g.conn = conn
	g.channel = 5
	return g, conn
}
func listener(b *Bot, guildID, user snowflake.ID, bot bool) {
	b.client.Caches.AddMember(d.Member{GuildID: guildID, User: d.User{ID: user, Bot: bot}})
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: guildID, UserID: user, ChannelID: new(snowflake.ID(5))})
}
func TestEmptyChannelTimeoutAndIsolation(t *testing.T) {
	b, _ := testBot(t)
	g, c := idleGuild(t, b, 1)
	other, otherConn := idleGuild(t, b, 2)
	listener(b, 1, 9, true)
	listener(b, 2, 3, false)
	old := g.searchCtx
	ticket := g.player.Ticket()
	if err := g.player.EnqueueMany([]media.Track{{Title: "one"}, {Title: "two"}}, ticket); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	b.checkEmpty(1, g, now)
	if g.emptySince.IsZero() {
		t.Fatal("bots counted as listeners")
	}
	b.checkEmpty(1, g, now.Add(emptyChannelGrace-time.Nanosecond))
	if c.closed {
		t.Fatal("left before grace")
	}
	b.checkEmpty(1, g, now.Add(emptyChannelGrace))
	if !c.closed || g.conn != nil || g.channel != 0 || old.Err() == nil || !g.emptySince.IsZero() {
		t.Fatal("session retained")
	}
	state := g.player.State()
	if state.Current != nil || len(state.Queued) != 0 {
		t.Fatal(state)
	}
	if err := g.player.Enqueue(media.Track{}, ticket); err == nil {
		t.Fatal("stale search accepted")
	}
	b.checkEmpty(2, other, now.Add(emptyChannelGrace))
	if otherConn.closed || other.searchCtx.Err() != nil {
		t.Fatal("other guild affected")
	}
}
func TestRejoinCancelsEmptyDeadline(t *testing.T) {
	b, _ := testBot(t)
	g, c := idleGuild(t, b, 1)
	now := time.Now()
	b.checkEmpty(1, g, now)
	listener(b, 1, 3, false)
	b.checkEmpty(1, g, now.Add(emptyChannelGrace))
	if c.closed || !g.emptySince.IsZero() {
		t.Fatal("rejoin did not cancel deadline")
	}
	// Moving away starts an entirely new grace period.
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(6))})
	b.checkEmpty(1, g, now.Add(3*emptyChannelGrace))
	if c.closed || !g.emptySince.Equal(now.Add(3*emptyChannelGrace)) {
		t.Fatal("reused old deadline")
	}
}
func TestVoiceEventResetsBriefRejoin(t *testing.T) {
	b, _ := testBot(t)
	g, _ := idleGuild(t, b, 1)
	g.emptySince = time.Now()
	b.voiceWake = make(chan struct{}, 1)
	b.voiceStateChanged(&events.GuildVoiceStateUpdate{GenericGuildVoiceState: &events.GenericGuildVoiceState{VoiceState: d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(5))}, Member: d.Member{User: d.User{ID: 3}}}})
	select {
	case <-b.voiceWake:
	case <-time.After(time.Second):
		t.Fatal("missing event notification")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.emptySince.IsZero() {
		t.Fatal("brief rejoin lost")
	}
}
func TestManualLeaveCancelsIdleAndResummonIsClean(t *testing.T) {
	b, r := testBot(t)
	g, c := idleGuild(t, b, 1)
	now := time.Now()
	b.checkEmpty(1, g, now)
	listener(b, 1, 3, false)
	b.command(interaction(t, "leave", true))
	if !c.closed || !g.emptySince.IsZero() {
		t.Fatal(r.response)
	}
	next := &dummyConn{}
	if err := g.player.Attach(transport{conn: next}); err != nil {
		t.Fatal(err)
	}
	g.conn = next
	g.channel = 5
	b.client.Caches.RemoveVoiceState(1, 3)
	b.checkEmpty(1, g, now.Add(3*emptyChannelGrace))
	if next.closed {
		t.Fatal("old deadline evicted new session")
	}
}
func TestUnknownListenerAndShutdownAreConservative(t *testing.T) {
	b, _ := testBot(t)
	g, c := idleGuild(t, b, 1)
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 77, ChannelID: new(snowflake.ID(5))})
	b.checkEmpty(1, g, time.Now())
	if !g.emptySince.IsZero() {
		t.Fatal("unknown user treated as bot")
	}
	b.client.Caches.RemoveVoiceState(1, 77)
	now := time.Now()
	b.checkEmpty(1, g, now)
	b.cancel()
	b.checkEmpty(1, g, now.Add(emptyChannelGrace))
	if c.closed || !g.emptySince.IsZero() {
		t.Fatal("idle callback ran after shutdown")
	}
}

type readyGateway struct{ gateway.Gateway }

func (readyGateway) Status() gateway.Status    { return gateway.StatusReady }
func (readyGateway) Close(context.Context)     {}
func (r *recordingRest) Close(context.Context) {}

func TestWatchVoiceDisconnectPreservesPausedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := testBot(t)
		g, c := idleGuild(t, b, 1)
		b.client.Gateway = readyGateway{}
		if err := g.player.Enqueue(media.Track{Title: "song"}, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, err := g.player.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		g.emptySince = time.Now()
		old := g.searchCtx
		b.voiceWake = make(chan struct{}, 1)
		b.workers.Add(1)
		go b.watchVoice()
		// Persistent transport loss must retain the logical track.
		b.voiceWake <- struct{}{}
		synctest.Wait()
		g.mu.Lock()
		if c.closed {
			t.Error("transient cache loss disconnected voice")
		}
		g.mu.Unlock()
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if c.closed || g.conn == nil || old.Err() != nil || !g.player.State().VoiceSuspended || g.player.Length() != 1 {
			t.Fatal("disconnect lost preserved track")
		}
		b.Close(context.Background())
	})
}
func TestBotShutdownWhilePausedCancelsIdleWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := testBot(t)
		g, c := idleGuild(t, b, 1)
		if err := g.player.Enqueue(media.Track{Title: "song"}, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		_, _ = g.player.SetPaused(true)
		g.emptySince = time.Now()
		b.workers.Add(1)
		go b.watchVoice()
		b.Close(context.Background())
		if !c.closed || g.conn != nil || !g.emptySince.IsZero() || g.searchCtx.Err() == nil || g.player.State().Current != nil {
			t.Fatal("shutdown retained paused session")
		}
	})
}

func TestVoiceEventCachesBotWithGuildScope(t *testing.T) {
	b, _ := testBot(t)
	g, _ := idleGuild(t, b, 1)
	state := d.VoiceState{GuildID: 1, UserID: 9, ChannelID: new(snowflake.ID(5))}
	b.client.Caches.AddVoiceState(state)
	b.voiceWake = make(chan struct{}, 1)
	b.voiceStateChanged(&events.GuildVoiceStateUpdate{GenericGuildVoiceState: &events.GenericGuildVoiceState{VoiceState: state, Member: d.Member{User: d.User{ID: 9, Bot: true}}}})
	select {
	case <-b.voiceWake:
	case <-time.After(time.Second):
		t.Fatal("missing event")
	}
	if b.hasListeners(1, g) {
		t.Fatal("bot event member cached under wrong guild")
	}
}

func TestTransientGatewayStateRetainsPlayback(t *testing.T) {
	b, _ := testBot(t)
	g, c := idleGuild(t, b, 1)
	now := time.Now()
	if err := g.player.EnqueueMany([]media.Track{{Title: "one"}, {Title: "two"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	old := g.searchCtx
	if !b.checkVoiceHealth(1, g, now, false, false, false, false) {
		t.Fatal("missing unhealthy state")
	}
	if c.closed || old.Err() != nil {
		t.Fatal("transient reconnect destroyed playback")
	}
	if b.checkVoiceHealth(1, g, now.Add(10*time.Second), true, true, true, true) {
		t.Fatal("recovery not recognized")
	}
	if c.closed || old.Err() != nil || !g.unhealthySince.IsZero() {
		t.Fatal("recovery cleared playback")
	}
	// A confirmed move is also transport loss; only explicit leave clears.
	b.checkVoiceHealth(1, g, now.Add(11*time.Second), true, true, true, false)
	if c.closed || old.Err() != nil || !g.player.State().VoiceSuspended {
		t.Fatal("confirmed move lost playback")
	}
}
