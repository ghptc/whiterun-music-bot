package discord

import (
	"context"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestFIX025MainGatewayReconnectDoesNotSuspendHealthyVoice(t *testing.T) {
	b, _ := testBot(t)
	g, c := idleGuild(t, b, 1)
	now := time.Now()
	for _, offset := range []time.Duration{0, time.Second, 10 * time.Second, time.Hour} {
		b.checkVoiceHealth(1, g, now.Add(offset), false, true, true, true)
		if c.closed || g.player.State().VoiceSuspended {
			t.Fatal("main gateway interrupted independent Voice transport")
		}
	}
}
func TestFIX025BrokenUDPRecreatesSessionButRemovalCannotRejoin(t *testing.T) {
	b, _ := testBot(t)
	b.client.Gateway = readyGateway{}
	manager := &freshVoiceManager{}
	b.client.VoiceManager = manager
	g, _ := idleGuild(t, b, 1)
	old := &readyVoiceConn{status: voice.StatusReady}
	g.conn = old
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: b.client.ID(), ChannelID: new(snowflake.ID(5))})
	v := b.playbackTransport(1, g, old).(recoveringTransport)
	v.brokenUDP.Store(true)
	next, err := v.Recover(context.Background())
	if err != nil || next == nil || !old.closed.Load() || len(manager.created) != 1 {
		t.Fatal("failed UDP reused", err)
	}
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: b.client.ID()})
	if _, err = next.(player.RecoverableVoice).Recover(context.Background()); err == nil || len(manager.created) != 1 {
		t.Fatal("removed bot automatically rejoined")
	}
}
func TestFIX025ResumeDoesNotDuplicateAnnouncement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := testBot(t)
		g := b.getGuild(1)
		g.player.Close(context.Background())
		g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, v player.Voice) error {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		// bind without a REST worker: a duplicate announcement would remain queued.
		b.bindAnnouncements(g)
		g.announcementChannel.Store(9)
		if err := g.player.Attach(announcementVoice{}); err != nil {
			t.Fatal(err)
		}
		if err := g.player.Enqueue(media.Track{Title: "saved occurrence", ResumeOffset: time.Second}, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !g.player.State().CurrentStarted || len(g.announcements) != 0 {
			t.Fatal("resume announced a duplicate card")
		}
	})
}

type echoConn struct {
	voice.Conn
	started, release chan struct{}
	mu               sync.Mutex
	updates          int
}

func (c *echoConn) Open(context.Context, snowflake.ID, bool, bool) error {
	close(c.started)
	<-c.release
	return nil
}
func (c *echoConn) HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate) {
	c.mu.Lock()
	c.updates++
	c.mu.Unlock()
}
func TestFIX025LateLeaveEchoCannotCloseNewJoin(t *testing.T) {
	base := &echoConn{started: make(chan struct{}), release: make(chan struct{})}
	conn := &voiceConn{Conn: base, guildID: 1, userID: 2}
	done := make(chan error, 1)
	go func() { done <- conn.Open(context.Background(), 5, false, true) }()
	<-base.started
	conn.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{GuildID: 1, UserID: 99, ChannelID: new(snowflake.ID(5))})
	conn.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{GuildID: 1, UserID: 2})
	base.mu.Lock()
	updates := base.updates
	base.mu.Unlock()
	if updates != 0 {
		t.Fatal("old leave echo reached the new transport")
	}
	conn.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{GuildID: 1, UserID: 2, ChannelID: new(snowflake.ID(5))})
	conn.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{GuildID: 1, UserID: 2})
	base.mu.Lock()
	updates = base.updates
	base.mu.Unlock()
	if updates != 2 {
		t.Fatal("confirmed membership/removal was suppressed")
	}
	close(base.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type lossUDP struct {
	lifecycleUDP
	closed atomic.Bool
}

func (u *lossUDP) Close() error { u.closed.Store(true); return nil }

type lossConn struct {
	readyVoiceConn
	udp lossUDP
}

func (c *lossConn) UDP() voice.UDPConn { return &c.udp }
func TestFIX025InvalidGatewayClosesUDPWithoutLeavingGuild(t *testing.T) {
	base := &lossConn{}
	conn := newVoiceConn(1, 2, func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }, func() {}).(*voiceConn)
	conn.Conn = base
	conn.gatewayLost()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.dave.WaitShutdown(ctx); err != nil {
		t.Fatal("stale DAVE watchdog retained", err)
	}
	if !base.udp.closed.Load() || base.closed.Load() {
		t.Fatal("gateway loss emitted a full connection close before recovery classification")
	}
}
