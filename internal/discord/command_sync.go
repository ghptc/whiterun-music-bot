package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"reflect"
	"time"
)

// POST upserts by name/type, preserving IDs and all unrelated commands.
func (b *Bot) syncCommands(ctx context.Context, scope snowflake.ID) (err error) {
	started := time.Now()
	defer func() {
		if err != nil {
			b.log.Error("command_sync_failed", "guild_id", scope, "elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		}
	}()
	list := func() ([]d.ApplicationCommand, error) {
		if scope == 0 {
			return b.client.Rest.GetGlobalCommands(b.client.ApplicationID, false, rest.WithCtx(ctx))
		}
		return b.client.Rest.GetGuildCommands(b.client.ApplicationID, scope, false, rest.WithCtx(ctx))
	}
	existing, err := list()
	if err != nil {
		return fmt.Errorf("command inventory: %w", err)
	}
	for _, want := range scopedCommands(scope) {
		unchanged := false
		for _, got := range existing {
			if commandMatches(want, got) {
				unchanged = true
				break
			}
		}
		if unchanged {
			continue
		}

		var got d.ApplicationCommand
		if scope == 0 {
			got, err = b.client.Rest.CreateGlobalCommand(b.client.ApplicationID, want, rest.WithCtx(ctx))
		} else {
			got, err = b.client.Rest.CreateGuildCommand(b.client.ApplicationID, scope, want, rest.WithCtx(ctx))
		}
		if err != nil {
			return fmt.Errorf("publish %s: %w", want.CommandName(), err)
		}
		if !commandMatches(want, got) {
			return fmt.Errorf("published command mismatch: %s", want.CommandName())
		}
	}
	published, err := list()
	if err != nil {
		return fmt.Errorf("command readback: %w", err)
	}
	for _, want := range scopedCommands(scope) {
		found := false
		for _, got := range published {
			if commandMatches(want, got) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("command readback mismatch: %s", want.CommandName())
		}
	}
	b.log.Info("command_sync_complete", "guild_id", scope, "count", len(commands()), "elapsed_ms", time.Since(started).Milliseconds())
	return nil
}
func commandMatches(want d.ApplicationCommandCreate, got d.ApplicationCommand) bool {
	// Compare every field sent by us, ignoring server-generated IDs/versions.
	a, _ := json.Marshal(want)
	c, _ := json.Marshal(got)
	var x, y map[string]any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(c, &y) != nil {
		return false
	}
	for key, value := range x {
		if !reflect.DeepEqual(value, y[key]) {
			return false
		}
	}
	return true
}

func scopedCommands(scope snowflake.ID) []d.ApplicationCommandCreate {
	result := commands()
	if scope != 0 {
		for i, c := range result {
			slash := c.(d.SlashCommandCreate)
			slash.Contexts = nil
			result[i] = slash
		}
	}
	return result
}

// SyncCommands publishes and verifies the configured scope without joining voice.
func (b *Bot) SyncCommands(ctx context.Context, scope snowflake.ID) error {
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return b.syncCommands(bounded, scope)
}
