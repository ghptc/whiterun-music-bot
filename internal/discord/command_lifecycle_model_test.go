package discord

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
)

// Independent value-only oracle: no queueEntry or player helpers. Random
// permutation is observed only after checking that its multiset is unchanged.
type lifecycleOracle struct {
	queue, history             []string
	current, loop              string
	paused, autoplay, attached bool
	generation                 uint64
}

func (m *lifecycleOracle) settle() {
	if m.attached && m.current == "" && len(m.queue) > 0 {
		m.current = m.queue[0]
		m.queue = m.queue[1:]
		m.paused = false
		m.history = append(m.history, m.current)
		if len(m.history) > 100 {
			m.history = m.history[len(m.history)-100:]
		}
	}
}
func (m *lifecycleOracle) command(f *lifecycleFixture, name string) {
	switch name {
	case "summon", "play":
		if !m.attached {
			m.attached = true
			m.generation++
		}
		if name == "play" {
			m.queue = append(m.queue, "request")
		}
	case "pause", "resume":
		if m.current != "" {
			m.paused = name == "pause"
		}
	case "remove":
		if len(m.queue) >= 2 {
			m.queue = append(m.queue[:1], m.queue[2:]...)
		}
	case "move":
		if len(m.queue) >= 2 {
			m.queue[0], m.queue[1] = m.queue[1], m.queue[0]
		}
	case "shuffle":
		got := lifecycleTitles(f.g.player.State().Queued)
		a, b := slices.Clone(m.queue), slices.Clone(got)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			f.t.Fatal("shuffle changed multiset", a, b)
		}
		m.queue = got
	case "jump":
		if len(m.queue) >= 2 {
			m.current = ""
			m.paused = false
			m.queue = m.queue[1:]
		}
	case "skip":
		if m.current != "" {
			m.current = ""
			m.paused = false
		} else if len(m.queue) > 0 {
			m.queue = m.queue[1:]
		}
	case "replay":
		if m.current != "" {
			m.queue = append([]string{m.current}, m.queue...)
			m.current = ""
			m.paused = false
		}
	case "previous":
		n := len(m.history)
		if m.current != "" {
			n--
		}
		if n > 0 && m.attached {
			prefix := []string{m.history[n-1]}
			if m.current != "" {
				prefix = append(prefix, m.current)
			}
			m.queue = append(prefix, m.queue...)
			m.history = m.history[:n-1]
			m.current = ""
			m.paused = false
		}
	case "loop":
		m.loop = "queue"
	case "autoplay":
		m.autoplay = true
	case "clear":
		m.queue = nil
		m.loop = "off"
		m.generation++
	case "stop", "leave":
		m.queue = nil
		m.current = ""
		m.paused = false
		m.loop = "off"
		m.generation++
		if name == "leave" {
			m.attached = false
		}
	}
	m.settle()
}
func lifecycleTitles(tracks []media.Track) []string {
	out := []string{}
	for _, tr := range tracks {
		out = append(out, tr.Title)
	}
	return out
}
func (f *lifecycleFixture) check(stage string) {
	f.t.Helper()
	s := f.g.player.State()
	if f.overlap.Load() {
		f.t.Fatal("overlapping playback resources")
	}
	wantActive := int64(0)
	if f.model.current != "" {
		wantActive = 1
	}
	if f.active.Load() != wantActive {
		f.t.Fatal("playback cleanup mismatch", f.active.Load(), wantActive)
	}
	current := ""
	if s.Current != nil {
		current = s.Current.Title
	}
	m := &f.model
	if current != m.current || !slices.Equal(lifecycleTitles(s.Queued), m.queue) || !slices.Equal(lifecycleTitles(f.g.player.History()), m.history) || s.Loop != m.loop || s.Paused != m.paused || s.Autoplay != m.autoplay || f.g.player.Ticket() != m.generation || s.CurrentStarted != (m.current != "") {
		f.t.Fatalf("%s state mismatch: current=%q queue=%v history=%v loop=%q paused=%v autoplay=%v generation=%d oracle=%+v", stage, current, lifecycleTitles(s.Queued), lifecycleTitles(f.g.player.History()), s.Loop, s.Paused, s.Autoplay, f.g.player.Ticket(), m)
	}
	saved, err := f.store.LoadGuild(context.Background(), "1")
	if err != nil {
		f.t.Fatal(err)
	}
	savedCurrent := ""
	if saved.Current != nil {
		savedCurrent = saved.Current.Title
	}
	if savedCurrent != m.current || !slices.Equal(lifecycleTitles(saved.Queue), m.queue) || !slices.Equal(lifecycleTitles(saved.History), m.history) || saved.Loop != m.loop || saved.Autoplay != m.autoplay || saved.CurrentStarted != (m.current != "") {
		f.t.Fatalf("%s durable mismatch: %+v oracle=%+v", stage, saved, m)
	}
}
func TestQA013OrderedCommandPairs(t *testing.T) {
	for _, phase := range []string{"idle", "playing", "paused"} {
		for _, a := range commands() {
			for _, b := range commands() {
				nameA, nameB := a.CommandName(), b.CommandName()
				t.Run(fmt.Sprintf("%s/%s/%s", phase, nameA, nameB), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						f := newLifecycleFixture(t, phase, nil)
						f.request(nameA)
						f.request(nameB)
						f.eof()
						// A subsequent accepted request must still play correctly after destructive
						// or rejected commands. Validate another transition, then complete teardown.
						f.request("play")
						f.eof()
						f.request("stop")
						if f.g.player.State().Current != nil || f.g.player.Length() != 0 {
							t.Fatal("cleanup left playable state")
						}
					})
				})
			}
		}
	}
}
