package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/synctest"

	"whiterun/internal/logstore"
)

func TestQA013CommandToTransitionPersistentCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	logs, err := logstore.Open(logstore.Options{Path: path, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close(context.Background())
	log := slog.New(logstore.NewHandler(slog.NewJSONHandler(io.Discard, nil), logs))
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixtureLog(t, "idle", nil, log)
		f.request("play")
		f.request("play")
		f.eof()
		f.request("stop")
		f.g.player.Close(context.Background())
	})
	if err := logs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := logstore.Read(context.Background(), path, logstore.Filter{Last: 10000}, &output); err != nil {
		t.Fatal(err)
	}
	type observed struct {
		event   string
		payload map[string]any
	}
	events := []observed{}
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var row logstore.Record
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, observed{row.Event, payload})
	}
	attempts := map[string]map[string]bool{}
	received, acked := map[string]bool{}, map[string]bool{}
	mutation := false
	for _, event := range events {
		p := event.payload
		if id, ok := p["interaction_id"].(string); ok {
			switch event.event {
			case "command_received":
				received[id] = true
			case "command_ack_complete":
				if p["error"] != nil {
					t.Fatal("unexpected acknowledgement error")
				}
				acked[id] = true
			}
		}
		if event.event == "queue_mutation" && p["action"] == "enqueue" {
			if p["queue_generation"] == nil || p["queue_version"] == nil || p["queue_size"] == nil {
				t.Fatal("mutation missing causal fields", p)
			}
			mutation = true
		}
		if id, ok := p["playback_attempt_id"].(string); ok {
			if attempts[id] == nil {
				attempts[id] = map[string]bool{}
			}
			attempts[id][event.event] = true
			if p["guild_id"] != "1" || p["queue_generation"] == nil {
				t.Fatal("attempt lost guild/generation", p)
			}
		}
	}
	if !mutation || len(attempts) != 2 {
		t.Fatal("missing mutation/attempts", mutation, attempts)
	}
	for id, stages := range attempts {
		for _, stage := range []string{"scheduler_advance", "track_playback_start", "track_playback_end", "playback_attempt_terminal", "player_transition"} {
			if !stages[stage] {
				t.Errorf("attempt=%s missing %s", id, stage)
			}
		}
	}
	if len(received) != 3 || len(acked) != 3 {
		t.Fatal("interaction identity lost", received, acked)
	}
	for id := range received {
		if !acked[id] {
			t.Error("received command missing acknowledgement", id)
		}
	}
}
