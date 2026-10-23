// DAVE opcode dispatch adapted from disgo v0.19.6 voice/gateway.go (Apache-2.0).
// Modified lifecycle: one session, bounded writes, synchronized heartbeat, no resume.
// See licenses/disgo-Apache-2.0.txt.
package discord

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/json/v2"
	"github.com/gorilla/websocket"
)

// incidentGateway deliberately has no reconnect loop. A websocket or heartbeat
// failure terminates this session; the guild circuit owns fresh negotiation.
// The stock gateway reconnects with context.Background even on heartbeat loss,
// which can outlive Close and reuse the invalid DAVE session.
type incidentGateway struct {
	mu           sync.Mutex
	writeMu      sync.Mutex
	conn         *websocket.Conn
	status       voice.Status
	ssrc         uint32
	seq          int
	nonce        int64
	awaitingACK  bool
	sent, ack    time.Time
	cancel       context.CancelFunc
	closed       bool
	log          *slog.Logger
	dave         godave.Session
	event        voice.EventHandlerFunc
	closeHandler voice.CloseHandlerFunc
	dialer       *websocket.Dialer
}

func incidentGatewayFactory(log *slog.Logger) voice.GatewayCreateFunc {
	return func(session godave.Session, event voice.EventHandlerFunc, closed voice.CloseHandlerFunc, _ ...voice.GatewayConfigOpt) voice.Gateway {
		return &incidentGateway{log: log.With("name", "voice_incident_gateway"), dave: session, event: event, closeHandler: closed, dialer: websocket.DefaultDialer}
	}
}
func (g *incidentGateway) Status() voice.Status { g.mu.Lock(); defer g.mu.Unlock(); return g.status }
func (g *incidentGateway) SSRC() uint32         { g.mu.Lock(); defer g.mu.Unlock(); return g.ssrc }
func (g *incidentGateway) Latency() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ack.Sub(g.sent)
}
func (g *incidentGateway) Open(ctx context.Context, state voice.State) error {
	g.mu.Lock()
	if g.conn != nil || g.closed {
		g.mu.Unlock()
		return errors.New("voice session already opened or closed")
	}
	g.status = voice.StatusConnecting
	g.mu.Unlock()
	conn, response, err := g.dialer.DialContext(ctx, fmt.Sprintf("wss://%s?v=%d", state.Endpoint, voice.GatewayVersion), nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return err
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		conn.Close()
		return context.Canceled
	}
	g.conn = conn
	life, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.status = voice.StatusWaitingForHello
	g.mu.Unlock()
	if state.ChannelID == nil {
		g.Close()
		return errors.New("voice channel missing")
	}
	g.dave.SetChannelID(godave.ChannelID(*state.ChannelID))
	ready := make(chan error, 1)
	var once sync.Once
	notify := func(err error) { once.Do(func() { ready <- err }) }
	go func() {
		err := g.listen(life, conn, state, notify)
		notify(err)
		g.mu.Lock()
		unexpected := !g.closed
		g.mu.Unlock()
		if unexpected {
			g.log.Warn("voice_gateway_closed", "guild_id", state.GuildID, "error", err, "action", "fresh session required")
			g.Close()
			// Conn's handler may synchronously close the gateway again.
			if g.closeHandler != nil {
				g.closeHandler(g, err, false)
			}
		}
	}()
	select {
	case <-ctx.Done():
		g.Close()
		return ctx.Err()
	case err := <-ready:
		return err
	}
}
func (g *incidentGateway) Close() { g.CloseWithCode(websocket.CloseNormalClosure, "session closed") }
func (g *incidentGateway) CloseWithCode(code int, message string) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.status = voice.StatusDisconnected
	conn := g.conn
	if g.cancel != nil {
		g.cancel()
	}
	g.mu.Unlock()
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, message), time.Now().Add(time.Second))
		_ = conn.Close()
	}
}
func (g *incidentGateway) Send(ctx context.Context, op voice.Opcode, data voice.GatewayMessageData) error {
	if g.Status() != voice.StatusReady {
		return d.ErrShardNotReady
	}
	return g.send(ctx, op, data)
}
func (g *incidentGateway) send(ctx context.Context, op voice.Opcode, data voice.GatewayMessageData) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var payload []byte
	kind := websocket.TextMessage
	var err error
	if op.IsBinary() {
		kind = websocket.BinaryMessage
		buf := new(bytes.Buffer)
		if err = binary.Write(buf, binary.BigEndian, uint8(op)); err == nil {
			err = binary.Write(buf, binary.BigEndian, data)
		}
		payload = buf.Bytes()
	} else {
		payload, err = json.Marshal(voice.GatewayMessage{Op: op, D: data})
	}
	if err != nil {
		return err
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	g.mu.Lock()
	conn, closed := g.conn, g.closed
	g.mu.Unlock()
	if conn == nil || closed {
		return errors.New("voice session closed")
	}
	deadline := time.Now().Add(3 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err = conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return conn.WriteMessage(kind, payload)
}
func (g *incidentGateway) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		g.mu.Lock()
		if g.awaitingACK {
			g.mu.Unlock()
			g.log.Warn("voice_heartbeat_timeout")
			// Break the reader; its sole failure handler owns close and notification.
			_ = conn.Close()
			return
		}
		g.nonce = time.Now().UnixMilli()
		g.sent = time.Now()
		g.awaitingACK = true
		data := voice.GatewayMessageDataHeartbeat{T: g.nonce, SeqAck: g.seq}
		g.mu.Unlock()
		if err := g.send(ctx, voice.OpcodeHeartbeat, data); err != nil {
			_ = conn.Close()
			return
		}
	}
}
func (g *incidentGateway) listen(ctx context.Context, conn *websocket.Conn, state voice.State, ready func(error)) error {
	hello := false
	for {
		kind, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var message voice.GatewayMessage
		if kind == websocket.BinaryMessage {
			err = message.UnmarshalBinary(bytes.NewReader(payload))
		} else {
			err = json.Unmarshal(payload, &message)
		}
		if err != nil {
			return fmt.Errorf("voice protocol: %w", err)
		}
		g.mu.Lock()
		if message.Seq > 0 {
			g.seq = message.Seq
		}
		g.mu.Unlock()
		switch message.Op {
		case voice.OpcodeHello:
			if hello {
				return errors.New("duplicate voice hello")
			}
			hello = true
			data := message.D.(voice.GatewayMessageDataHello)
			interval := time.Duration(data.HeartbeatInterval) * time.Millisecond
			if interval <= 0 {
				return errors.New("invalid voice heartbeat interval")
			}
			g.mu.Lock()
			g.status = voice.StatusIdentifying
			g.mu.Unlock()
			err := g.send(ctx, voice.OpcodeIdentify, voice.GatewayMessageDataIdentify{GuildID: state.GuildID, UserID: state.UserID, SessionID: state.SessionID, Token: state.Token, MaxDaveProtocolVersion: g.dave.MaxSupportedProtocolVersion()})
			if err != nil {
				return err
			}
			go g.heartbeat(ctx, conn, interval)
		case voice.OpcodeReady:
			data := message.D.(voice.GatewayMessageDataReady)
			g.mu.Lock()
			g.status = voice.StatusReady
			g.ssrc = data.SSRC
			g.mu.Unlock()
			ready(nil)
		case voice.OpcodeHeartbeatACK:
			data := message.D.(voice.GatewayMessageDataHeartbeatACK)
			g.mu.Lock()
			valid := g.awaitingACK && data.T == g.nonce
			if valid {
				g.awaitingACK = false
				g.ack = time.Now()
			}
			g.mu.Unlock()
			if !valid {
				return errors.New("invalid voice heartbeat ACK")
			}
		case voice.OpcodeSessionDescription:
			d := message.D.(voice.GatewayMessageDataSessionDescription)
			g.dave.OnSelectProtocolAck(uint16(d.DaveProtocolVersion))

		case voice.OpcodeClientsConnect:
			d := message.D.(voice.GatewayMessageDataClientsConnect)

			for _, userID := range d.UserIDs {
				g.dave.AddUser(godave.UserID(userID.String()))
			}

		case voice.OpcodeClientDisconnect:
			d := message.D.(voice.GatewayMessageDataClientDisconnect)
			g.dave.RemoveUser(godave.UserID(d.UserID.String()))

		case voice.OpcodeDavePrepareTransition:
			d := message.D.(voice.GatewayMessageDataDaveProtocolPrepareTransition)
			g.dave.OnDavePrepareTransition(d.TransitionID, d.ProtocolVersion)

		case voice.OpcodeDaveExecuteTransition:
			d := message.D.(voice.GatewayMessageDataDaveProtocolExecuteTransition)
			g.dave.OnDaveExecuteTransition(d.TransitionID)

		case voice.OpcodeDavePrepareEpoch:
			d := message.D.(voice.GatewayMessageDataDaveProtocolPrepareEpoch)
			g.dave.OnDavePrepareEpoch(d.Epoch, d.ProtocolVersion)

		case voice.OpcodeDaveMLSExternalSenderPackage:
			g.dave.OnDaveMLSExternalSenderPackage(message.D.(voice.GatewayMessageDataDaveMLSExternalSenderPackage))

		case voice.OpcodeDaveMLSProposals:
			g.dave.OnDaveMLSProposals(message.D.(voice.GatewayMessageDataDaveMLSProposals))

		case voice.OpcodeDaveMLSPrepareCommitTransition:
			d := message.D.(voice.GatewayMessageDataDaveMLSAnnounceCommitTransition)
			g.dave.OnDaveMLSPrepareCommitTransition(d.TransitionID, d.CommitMessage)

		case voice.OpcodeDaveMLSWelcome:
			d := message.D.(voice.GatewayMessageDataDaveMLSWelcome)
			g.dave.OnDaveMLSWelcome(d.TransitionID, d.WelcomeMessage)
		}
		if g.event != nil {
			g.event(g, message.Op, message.Seq, message.D)
		}
	}
}
