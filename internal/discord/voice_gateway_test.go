package discord

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"whiterun/internal/player"
)

func TestIncidentGatewayFreshIdentifyAndNoBackgroundReconnect(t *testing.T) {
	for _, fault := range []string{"4006", "heartbeat", "explicit_close"} {
		t.Run(fault, func(t *testing.T) {
			var connections, identifies, resumes atomic.Int32
			serverDone := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections.Add(1)
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
				_ = ws.WriteJSON(map[string]any{"op": 8, "d": map[string]any{"heartbeat_interval": 30}})
				var command struct {
					Op int `json:"op"`
				}
				if ws.ReadJSON(&command) != nil {
					return
				}
				if command.Op == 0 {
					identifies.Add(1)
				}
				if command.Op == 7 {
					resumes.Add(1)
				}
				_ = ws.WriteJSON(map[string]any{"op": 2, "d": map[string]any{"ssrc": 123, "ip": "127.0.0.1", "port": 9999, "modes": []string{"aead_xchacha20_poly1305_rtpsize"}}})
				if fault == "4006" {
					_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4006, "Session is no longer valid."), time.Now().Add(time.Second))
					return
				}
				for {
					if _, _, err = ws.ReadMessage(); err != nil {
						break
					}
				}
			}))
			defer server.Close()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			session := godave.NewNoopSession(log, "1", nil)
			var closed atomic.Int32
			g := incidentGatewayFactory(log)(session, nil, func(voice.Gateway, error, bool) { closed.Add(1); close(serverDone) }).(*incidentGateway)
			g.dialer = &websocket.Dialer{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			channel := snowflake.ID(3)
			if err := g.Open(ctx, voice.State{GuildID: 1, UserID: 2, ChannelID: &channel, Endpoint: strings.TrimPrefix(server.URL, "https://"), SessionID: "fresh", Token: "fixture"}); err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if fault == "explicit_close" {
				g.Close()
				g.Close()
				if err := g.Send(ctx, voice.OpcodeSpeaking, voice.GatewayMessageDataSpeaking{}); err == nil {
					t.Fatal("closed session accepted speaking")
				}
				time.Sleep(100 * time.Millisecond)
				if closed.Load() != 0 {
					t.Fatal("explicit close triggered recovery callback")
				}
			} else {
				select {
				case <-serverDone:
				case <-ctx.Done():
					t.Fatal("session failure did not terminate", fault)
				}
				if g.Status() != voice.StatusDisconnected || closed.Load() != 1 {
					t.Fatal("loss not terminal")
				}
			}
			if connections.Load() != 1 || identifies.Load() != 1 || resumes.Load() != 0 {
				t.Fatalf("invalid session resumed: connections=%d identify=%d resume=%d", connections.Load(), identifies.Load(), resumes.Load())
			}
		})
	}
}

func TestVoiceIncidentAnnouncementHasNoTrackFailureLabel(t *testing.T) {
	event := player.PlaybackEvent{FailureStage: "voice", SkippedReason: "Discord Voice interrupted. The track and queue are preserved.", State: player.Suspended}
	message := playbackMessage(event)
	if message.Content != event.SkippedReason || strings.Contains(message.Content, "Unavailable") || strings.Contains(message.Content, "failed to start") {
		t.Fatal(message.Content)
	}
}

type readyVoiceGateway struct {
	voice.Gateway
	status voice.Status
}

func (g readyVoiceGateway) Status() voice.Status { return g.status }

type readyVoiceConn struct {
	lifecycleConn
	status voice.Status
}

func (c *readyVoiceConn) Gateway() voice.Gateway { return readyVoiceGateway{status: c.status} }

type freshVoiceManager struct {
	voice.Manager
	created []*readyVoiceConn
}

func (m *freshVoiceManager) CreateConn(snowflake.ID) voice.Conn {
	c := &readyVoiceConn{status: voice.StatusReady}
	m.created = append(m.created, c)
	return c
}

func TestVoiceRecoveryReplacesConnectionAndProvesReadiness(t *testing.T) {
	b, _ := testBot(t)
	b.client.Gateway = readyGateway{}
	manager := &freshVoiceManager{}
	b.client.VoiceManager = manager
	g, old := idleGuild(t, b, 1)
	b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: b.client.ID(), ChannelID: new(snowflake.ID(5))})
	v := b.playbackTransport(1, g, old)
	replacement, err := v.(player.RecoverableVoice).Recover(context.Background())
	if err != nil || replacement == nil || !old.closed || len(manager.created) != 1 || g.conn != manager.created[0] {
		t.Fatalf("fresh recovery: %v", err)
	}
	replacement, err = replacement.(player.RecoverableVoice).Recover(context.Background())
	if err != nil || replacement == nil || len(manager.created) != 1 || manager.created[0].closed.Load() || g.conn != manager.created[0] {
		t.Fatalf("healthy session was not preserved: %v", err)
	}
	if err := confirmVoiceReady(context.Background(), &readyVoiceConn{status: voice.StatusDisconnected}); err == nil {
		t.Fatal("false readiness accepted")
	}
}

func TestVoiceRecoveryCancellationDoesNotWaitForOrchestrationLock(t *testing.T) {
	b, _ := testBot(t)
	g, old := idleGuild(t, b, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.mu.Lock()
	_, err := b.playbackTransport(1, g, old).(player.RecoverableVoice).Recover(ctx)
	g.mu.Unlock()
	if err != context.Canceled {
		t.Fatal(err)
	}
}

func TestVoiceConnectionsHaveDistinctDAVESessions(t *testing.T) {
	update := func(context.Context, snowflake.ID, *snowflake.ID, bool, bool) error { return nil }
	first := newVoiceConn(1, 2, update, func() {}).(*voiceConn)
	second := newVoiceConn(1, 2, update, func() {}).(*voiceConn)
	defer first.Close(context.Background())
	defer second.Close(context.Background())
	if first.dave == nil || second.dave == nil || first.dave == second.dave {
		t.Fatal("DAVE session reused")
	}
	first.Close(context.Background())
	first.Close(context.Background())
}
