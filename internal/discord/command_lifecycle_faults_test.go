package discord

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type lifecycleFailReply struct {
	rest.Rest
	attempts atomic.Int64
}

func (r *lifecycleFailReply) UpdateInteractionResponse(snowflake.ID, string, d.MessageUpdate, ...rest.RequestOpt) (*d.Message, error) {
	r.attempts.Add(1)
	return nil, errors.New("injected reply HTTP failure")
}
func TestQA013CommandAcknowledgementAndReplyFaults(t *testing.T) {
	for _, command := range commands() {
		for _, stage := range []string{"ack", "reply"} {
			t.Run(command.CommandName()+"/"+stage, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newLifecycleFixture(t, "playing", nil)
					name := command.CommandName()
					e := f.interaction(name)
					ack := 0
					before := f.g.player.State()
					ticket := f.g.player.Ticket()
					e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error {
						ack++
						if stage == "ack" {
							return errors.New("injected acknowledgement failure")
						}
						return nil
					}
					original := f.b.client.Rest
					broken := &lifecycleFailReply{Rest: original}
					if stage == "reply" {
						f.b.client.Rest = broken
					}
					f.b.command(e)
					synctest.Wait()
					if ack != 1 {
						t.Fatal("ack retry/omission", ack)
					}
					if stage == "ack" {
						if !reflect.DeepEqual(before, f.g.player.State()) || ticket != f.g.player.Ticket() {
							t.Fatal("failed ack executed command")
						}
					} else {
						f.model.command(f, name)
						synctest.Wait()
						f.check("failed response")
						wantAttempts := int64(1)
						if name == "queue" {
							wantAttempts = 2
						} // original PATCH plus bounded failure notice
						if broken.attempts.Load() != wantAttempts {
							t.Fatal("reply retry/omission", broken.attempts.Load())
						}
						f.b.client.Rest = original
					}
					f.eof()
					f.request("play")
					f.eof()
					f.request("stop")
				})
			})
		}
	}
}
func TestQA013PermissionRejectionPreservesSubsequentPlayback(t *testing.T) {
	for _, name := range []string{"summon", "play", "pause", "resume", "previous", "skip", "clear", "stop", "leave", "remove", "move", "shuffle", "jump", "replay", "loop", "seek", "autoplay"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, "playing", nil)
				f.b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(6))})
				e := f.interaction(name)
				e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
				f.b.command(e)
				synctest.Wait()
				f.check("permission rejected")
				listener(f.b, 1, 3, false)
				f.eof()
				f.request("skip")
				f.request("stop")
			})
		})
	}
}

// Malformed options that Discord normally filters are deliberately delivered to
// the handler. Rejection must be atomic and must not affect the next EOF.
func TestQA013InvalidArgumentsContinuePlayback(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []map[string]any
	}{
		{"skip", []map[string]any{{"name": "count", "type": 4, "value": 0}}},
		{"remove", []map[string]any{{"name": "position", "type": 4, "value": -1}}},
		{"move", []map[string]any{{"name": "from", "type": 4, "value": 99}, {"name": "to", "type": 4, "value": 1}}},
		{"jump", []map[string]any{{"name": "position", "type": 4, "value": 1000}}},
		{"loop", []map[string]any{{"name": "mode", "type": 3, "value": "invalid"}}},
		{"seek", []map[string]any{{"name": "position", "type": 3, "value": "invalid"}}},
		{"history", []map[string]any{{"name": "limit", "type": 4, "value": 21}}},
		{"play", []map[string]any{{"name": "query", "type": 3, "value": "song"}, {"name": "force", "type": 5, "value": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, "playing", nil)
				e := f.interaction(tc.name)
				data := e.Data.(d.SlashCommandInteractionData)
				raw, _ := json.Marshal(tc.opts)
				var options []d.SlashCommandOption
				if err := json.Unmarshal(raw, &options); err != nil {
					t.Fatal(err)
				}
				data.Options = map[string]d.SlashCommandOption{}
				for _, o := range options {
					data.Options[o.Name] = o
				}
				e.Data = data
				e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
				f.b.command(e)
				synctest.Wait()
				f.check("invalid arguments")
				f.eof()
				f.request("stop")
			})
		})
	}
}
