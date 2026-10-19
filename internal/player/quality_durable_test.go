package player

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"whiterun/internal/media"
)

type qualityStore struct {
	state                 DurableState
	readError, writeError error
}

func (s *qualityStore) LoadGuild(context.Context, string) (DurableState, error) {
	return s.state, s.readError
}
func (s *qualityStore) SaveGuild(ctx context.Context, _ string, state DurableState) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("persistence is unbounded")
	}
	if s.writeError != nil {
		return s.writeError
	}
	s.state = state
	return nil
}
func TestQualityDurableRecovery(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "not-started"
		if started {
			name = "started"
		}
		t.Run(name, func(t *testing.T) {
			current := media.Track{Title: "current", Artists: []string{"artist"}, Explicit: new(true)}
			history := []media.Track{{Title: "past"}}
			if started {
				history = append(history, current)
			}
			store := &qualityStore{state: DurableState{Loop: "queue", Autoplay: true, Current: &current, CurrentStarted: started, Queue: []media.Track{{Title: "next"}}, History: history}}
			p := qualityDetached(t)
			if err := p.ConfigureStore(store, "1"); err != nil {
				t.Fatal(err)
			}
			state := p.State()
			if state.Current != nil || state.Loop != "queue" || !state.Autoplay || len(state.Queued) != 2 || state.Queued[0].Title != "current" || state.Queued[1].Title != "next" {
				t.Fatal("recovery changed logical order", state)
			}
			if got := p.History(); len(got) != 1 || got[0].Title != "past" {
				t.Fatal("recovery duplicated current history", got)
			}
			current.Artists[0] = "external"
			*current.Explicit = false
			if got := p.State().Queued[0]; got.Artists[0] != "artist" || !*got.Explicit {
				t.Fatal("store aliases restored metadata")
			}
			store.writeError = errors.New("disk full")
			if err := p.Move(1, 2); err != nil {
				t.Fatal(err)
			}
			if p.State().PersistenceError == "" {
				t.Fatal("persistence failure hidden")
			}
			store.writeError = nil
			if err := p.Move(2, 1); err != nil {
				t.Fatal(err)
			}
			if p.State().PersistenceError != "" {
				t.Fatal("persistence warning not cleared")
			}
			if len(store.state.Queue) != 2 || store.state.Queue[0].Title != "current" {
				t.Fatal("recovery not persisted")
			}
		})
	}
}
func TestQualityDurableReadFailure(t *testing.T) {
	p := qualityDetached(t)
	before := p.State()
	if err := p.ConfigureStore(&qualityStore{readError: errors.New("database unavailable")}, "1"); err == nil {
		t.Fatal("load failure hidden")
	}
	if !reflect.DeepEqual(before, p.State()) {
		t.Fatal("failed restore changed state")
	}
}
