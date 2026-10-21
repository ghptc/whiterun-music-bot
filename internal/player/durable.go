package player

import (
	"context"
	"errors"
	"time"
	"whiterun/internal/media"
)

// DurableState contains metadata only. Current restarts from the beginning after
// explicit voice attachment; unresolved Spotify entries are matched on playback.
const persistenceTimeout = 3 * time.Second

type DurableState struct {
	Loop           string
	Autoplay       bool
	Current        *media.Track
	CurrentStarted bool
	Queue          []media.Track
	History        []media.Track
}
type PlaybackRecorder interface {
	RecordPlayback(context.Context, string, media.Track) error
}
type Store interface {
	SaveGuild(context.Context, string, DurableState) error
	LoadGuild(context.Context, string) (DurableState, error)
}

func (p *GuildPlayer) ConfigureStore(store Store, guildID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.voice != nil || p.current != nil || len(p.queue) > 0 {
		return errors.New("configure persistence before attaching voice")
	}
	ctx, cancel := context.WithTimeout(p.ctx, persistenceTimeout)
	defer cancel()
	state, err := store.LoadGuild(ctx, guildID)
	if err != nil {
		return err
	}
	p.loop, p.autoplay = state.Loop, state.Autoplay
	tracks := state.Queue
	if state.Current != nil {
		tracks = append([]media.Track{*state.Current}, tracks...)
	}
	if len(tracks) > MaxQueueTracks+1 {
		return errors.New("durable queue exceeds capacity")
	}
	for _, t := range tracks {
		p.queue = append(p.queue, queueEntry{Track: copyTrack(t)})
	}
	p.store, p.guildID = store, guildID
	if state.CurrentStarted && state.Current != nil && len(state.History) > 0 {
		state.History = state.History[:len(state.History)-1]
	}
	for _, t := range state.History {
		p.history = append(p.history, copyTrack(t))
	}
	p.diagnosticLocked(0, "state_restored", "component", "player", "positions", len(p.queue), "automatic_playback", false)
	return nil
}
func (p *GuildPlayer) durableLocked() DurableState {
	s := DurableState{Loop: p.loop, Autoplay: p.autoplay, CurrentStarted: p.current != nil && p.startedSequence == p.sequence}
	if p.current != nil {
		t := copyTrack(*p.current)
		t.Timing = nil
		s.Current = &t
	}
	for _, e := range p.queue {
		t := copyTrack(e.Track)
		t.Timing = nil
		s.Queue = append(s.Queue, t)
	}
	for _, t := range p.history {
		t = copyTrack(t)
		t.Timing = nil
		s.History = append(s.History, t)
	}
	return s
}
func (p *GuildPlayer) persistLocked() {
	if p.store == nil || p.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	if err := p.store.SaveGuild(ctx, p.guildID, p.durableLocked()); err != nil {
		p.persistenceError = "Latest queue changes could not be saved; recovery may be incomplete."
		p.log.Error("durable_state_write_failed", "guild_id", p.guildID, "error", err)
		if !p.retryingPersistence {
			p.retryingPersistence = true
			p.persistenceWorkers.Add(1)
			go p.retryPersistence()
		}
	} else {
		p.persistenceError = ""
	}
}

// Retry only a dirty checkpoint, not a command. Persist the latest state under
// the same lock as queue mutations, with a finite budget and cancellation.
func (p *GuildPlayer) retryPersistence() {
	defer p.persistenceWorkers.Done()
	defer func() { p.mu.Lock(); p.retryingPersistence = false; p.mu.Unlock() }()
	for attempt := 0; attempt < 3; attempt++ {
		timer := time.NewTimer(time.Second * time.Duration(1<<attempt))
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		p.mu.Lock()
		if p.ctx.Err() != nil || p.store == nil || p.persistenceError == "" {
			p.mu.Unlock()
			return
		}
		p.persistLocked()
		ok := p.persistenceError == ""
		if ok {
			p.diagnosticLocked(0, "recovery_completed", "component", "checkpoint", "incident_id", p.sessionID+"/checkpoint", "attempt", attempt+1)
		}
		p.mu.Unlock()
		if ok {
			return
		}
	}
}
