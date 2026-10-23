package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"log/slog"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func TestFIX0251ThreeHoursMainGatewayOutagesPreserveVoice(t *testing.T) {
	b, _ := testBot(t)
	g, conn := idleGuild(t, b, 1)
	if err := g.player.EnqueueMany([]media.Track{{Title: "active"}, {Title: "pending"}}, g.player.Ticket()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for minute := range 181 {
		// Model the observed reconnects at 60/120/180 minutes, plus stale cache.
		ready := minute%60 != 0
		b.checkVoiceHealth(1, g, now.Add(time.Duration(minute)*time.Minute), ready, true, ready, ready)
		if conn.closed || g.player.State().VoiceSuspended {
			t.Fatal("healthy independent Voice interrupted", minute)
		}
	}
	g.player.Stop()
	if conn.closed {
		t.Fatal("stop unexpectedly left Voice")
	}
}

func TestFIX0251VoiceOutageGraceAndRecoveryDoesNotLeave(t *testing.T) {
	b, _ := testBot(t)
	g, conn := idleGuild(t, b, 1)
	now := time.Now()
	b.checkVoiceHealth(1, g, now, true, false, true, true)
	b.checkVoiceHealth(1, g, now.Add(4*time.Second), true, false, true, true)
	if g.player.State().VoiceSuspended || conn.closed {
		t.Fatal("transient outage caused teardown")
	}
	b.checkVoiceHealth(1, g, now.Add(5*time.Second), true, false, true, true)
	if !g.player.State().VoiceSuspended || conn.closed {
		t.Fatal("watchdog must suspend audio without leaving")
	}
	// Explicit teardown remains authorized; cancellation metadata must survive.
	g.player.Leave(context.Background())
}

func TestFIX0251CloseLogsOriginExactlyOnce(t *testing.T) {
	var output bytes.Buffer
	base := &readyVoiceConn{status: voice.StatusReady}
	c := &voiceConn{Conn: base, guildID: 1, joinChannel: 5, correlationID: "fixture/voice/1", log: slog.New(slog.NewJSONHandler(&output, nil))}
	ctx := player.VoiceCloseContext(context.Background(), "command_leave")
	c.Close(ctx)
	c.Close(ctx)
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatal("expected exactly one JSON event", err)
	}
	if record["msg"] != "voice_disconnect_initiated" || record["origin"] != "command_leave" || record["incident_id"] != "fixture/voice/1" || record["channel_id"] != "5" {
		t.Fatal(record)
	}
}

func TestFIX0251LocalLeaveEchoIsNotReportedAsRemoteDisconnect(t *testing.T) {
	var output bytes.Buffer
	base := &echoConn{Conn: &readyVoiceConn{status: voice.StatusReady}}
	c := &voiceConn{Conn: base, guildID: 1, userID: 2, joinChannel: 5, joined: true, correlationID: "fixture/voice/1", log: slog.New(slog.NewJSONHandler(&output, nil))}
	c.Close(player.VoiceCloseContext(context.Background(), "command_leave"))
	c.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{GuildID: 1, UserID: 2})
	if strings.Contains(output.String(), "voice_disconnect_remote") || !strings.Contains(output.String(), "local_close_echo") {
		t.Fatal("local close incorrectly attributed to remote", output.String())
	}
}
