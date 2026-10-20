package discord

import (
	"strings"
	"testing"
	"testing/synctest"
)

func TestQA014CommandMarathon(t *testing.T) {
	covered := map[string][]string{}
	for _, journey := range []struct {
		name string
		run  func(*testing.T) *marathonSession
	}{
		{"A_normal", marathonA}, {"B_progressive302_FIX014", marathonB}, {"C_queue_edits", marathonC}, {"D_advanced_controls", marathonD},
		{"E_multiple_users", marathonE}, {"F_restart", marathonF}, {"G_provider_failure", marathonG}, {"H_force_play", marathonH},
	} {
		t.Run(journey.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := journey.run(t)
				for name := range m.seen {
					covered[name] = append(covered[name], journey.name)
				}
			})
		})
	}
	for _, command := range commands() {
		if len(covered[command.CommandName()]) == 0 {
			t.Error("registered command absent from journeys", command.CommandName())
		}
	}
	if len(covered) != len(commands()) {
		t.Fatal("journeys used an unregistered command", covered)
	}
}
func marathonA(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{}
	m.command("summon", nil, 3, w)
	w = marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	w.queue = []string{"song002"}
	m.playSong(2, w)
	m.command("queue", nil, 3, w)
	w.paused = true
	m.command("pause", nil, 3, w)
	w.paused = false
	m.command("resume", nil, 3, w)
	m.command("now", nil, 3, w)
	w.current = "song002"
	w.queue = nil
	w.history = []string{"song001", "song002"}
	m.command("skip", nil, 3, w)
	w.current = "song001"
	w.queue = []string{"song002"}
	w.history = []string{"song001"}
	m.command("previous", nil, 3, w)
	w.history = append(w.history, "song001")
	m.command("replay", nil, 3, w)
	m.command("lyrics", nil, 3, w)
	m.command("help", nil, 3, w)
	w.current = ""
	w.queue = nil
	w.loop = "off"
	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	return m
}
func marathonC(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	tracks := marathonPlaylist(4)
	m.metadata(tracks)
	w.queue = marathonNames(tracks)
	m.command("play", marathonPlayOptions(1, true), 3, w)
	w.queue = []string{"p003", "p001", "p002", "p004"}
	m.command("move", []map[string]any{marathonOption("from", 4, 3), marathonOption("to", 4, 1)}, 3, w)
	w.queue = []string{"p003", "p002", "p004"}
	m.command("remove", nil, 3, w)
	w = m.command("shuffle", nil, 3, w)
	target := w.queue[1]
	w.current = target
	w.queue = w.queue[2:]
	w.history = append(w.history, target)
	m.command("jump", nil, 3, w)
	m.command("queue", nil, 3, w)
	w.queue = nil
	w.loop = "off"
	m.command("clear", nil, 3, w)
	w.queue = []string{"song002"}
	m.playSong(2, w)
	w.current = "song002"
	w.queue = nil
	w.history = append(w.history, "song002")
	m.eof(w)
	w.current = ""
	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	return m
}
func marathonD(t *testing.T) *marathonSession {
	m := newMarathon(t)
	w := marathonWant{current: "song001", history: []string{"song001"}}
	m.playSong(1, w)
	w.queue = []string{"song002"}
	m.playSong(2, w)
	m.command("seek", nil, 3, w)
	if !strings.Contains(m.r.response, "unavailable") {
		t.Fatal("seek unexpectedly available", m.r.response)
	}
	w.loop = "track"
	m.command("loop", []map[string]any{marathonOption("mode", 3, "track")}, 3, w)
	w.history = append(w.history, "song001")
	m.eof(w)
	w.loop = "queue"
	m.command("loop", nil, 3, w)
	w.current = "song002"
	w.queue = nil
	w.history = append(w.history, "song002")
	m.command("skip", nil, 3, w)
	w.loop = "off"
	m.command("loop", []map[string]any{marathonOption("mode", 3, "off")}, 3, w)
	w.autoplay = true
	m.command("autoplay", nil, 3, w)
	w.current = ""
	m.eof(w)
	m.command("history", nil, 3, w)

	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	return m
}
