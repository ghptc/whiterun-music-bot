package discord

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

func TestQualityCommandRegistry(t *testing.T) {
	names := strings.Fields("summon play now pause resume previous skip queue clear stop leave remove move shuffle jump replay loop seek autoplay lyrics history help")
	expected := map[string]bool{}
	for _, n := range names {
		expected[n] = true
	}
	seen := map[string]bool{}
	for _, cmd := range commands() {
		t.Run(cmd.CommandName(), func(t *testing.T) {
			name := cmd.CommandName()
			if !expected[name] || seen[name] {
				t.Fatal("unknown or duplicate command", name)
			}
			seen[name] = true
			raw, err := json.Marshal(cmd)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Description string `json:"description"`
				Contexts    []int  `json:"contexts"`
				Options     []struct {
					Name     string `json:"name"`
					Required bool   `json:"required"`
					Type     int    `json:"type"`
				} `json:"options"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body.Description == "" || len(body.Description) > 100 || !reflect.DeepEqual(body.Contexts, []int{0}) {
				t.Fatalf("invalid registration: %s", raw)
			}
			optional := false
			options := map[string]bool{}
			for _, o := range body.Options {
				if options[o.Name] {
					t.Fatal("duplicate option", o.Name)
				}
				options[o.Name] = true
				if optional && o.Required {
					t.Fatal("required option after optional")
				}
				optional = optional || !o.Required
				if name == "play" && o.Name == "query" && (!o.Required || o.Type != 3) {
					t.Fatal("query contract")
				}
				if name == "play" && o.Name == "force" && (o.Required || o.Type != 5) {
					t.Fatal("force contract")
				}
			}
			if name == "play" && (!options["query"] || !options["force"]) {
				t.Fatal("missing play option")
			}
		})
	}
	if len(seen) != len(expected) {
		t.Fatalf("got %d commands want %d", len(seen), len(expected))
	}
}
func TestQualityVoiceBoundary(t *testing.T) {
	for _, name := range strings.Fields("summon play pause resume previous skip clear stop leave remove move shuffle jump replay loop seek autoplay") {
		for _, mode := range []string{"absent", "other channel"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				b, r := testBot(t)
				g := b.getGuild(1)
				if mode == "other channel" {
					g.conn = &dummyConn{}
					g.channel = 5
					b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 3, ChannelID: new(snowflake.ID(6))})
				}
				before := g.player.State()
				ticket := g.player.Ticket()
				search := g.searchCtx
				e := interaction(t, name, true)
				e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
				b.command(e)
				if !strings.Contains(r.response, "Join ") {
					t.Fatal("voice boundary bypassed", r.response)
				}
				if !reflect.DeepEqual(before, g.player.State()) || ticket != g.player.Ticket() || search.Err() != nil {
					t.Fatal("unauthorized mutation")
				}
			})
		}
	}
}
func TestQualityAckFailure(t *testing.T) {
	for _, cmd := range commands() {
		t.Run(cmd.CommandName(), func(t *testing.T) {
			b, r := testBot(t)
			e := interaction(t, cmd.CommandName(), true)
			calls := 0
			e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error {
				calls++
				return errors.New("interaction expired or network failure")
			}
			b.command(e)
			if calls != 1 || len(b.guilds) != 0 || r.response != "" || len(r.followups) != 0 {
				t.Fatal("work occurred after failed acknowledgement")
			}
		})
	}
}
