package player

import (
	"errors"
	"fmt"
	"sort"

	"whiterun/internal/media"
)

// Snapshot returns copies, never exposing mutable queue state.
func (p *GuildPlayer) Snapshot() (*media.Track, []media.Track) {
	state := p.State()
	return state.Current, state.Queued
}

type State struct {
	PlaybackState    AttemptState
	CurrentStarted   bool
	PersistenceError string
	Imports          []CollectionProgress
	Current          *media.Track
	Queued           []media.Track
	Paused           bool
	Halted           bool
	ProviderBlocked  bool
	ProviderReason   string
	Entries          []QueueEntry
	Resolving        []CollectionProgress
}

// State takes one consistent snapshot of playback and upcoming tracks.
func (p *GuildPlayer) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	var current *media.Track
	if p.current != nil {
		t := copyTrack(*p.current)
		current = &t
	}
	state := State{CurrentStarted: p.current != nil && p.startedSequence == p.sequence, PersistenceError: p.persistenceError, Imports: append([]CollectionProgress(nil), p.completedImports...), Current: current, Paused: p.pause.paused(), Halted: p.halted, ProviderBlocked: p.providerBlocked, ProviderReason: p.providerReason}
	if current != nil {
		state.PlaybackState = Starting
		if state.CurrentStarted {
			state.PlaybackState = Playing
		}
	}
	for _, entry := range p.queue {
		phase := Ready
		if entry.Pending {
			phase = Pending
			if job := p.jobs[entry.jobID]; job != nil && job.searches[entry.position] != nil {
				phase = Resolving
			}
		}
		track := copyTrack(entry.Track)
		state.Entries = append(state.Entries, QueueEntry{State: phase, Track: track, Pending: entry.Pending, CollectionID: entry.jobID, Position: entry.position + 1})
		if !entry.Pending {
			state.Queued = append(state.Queued, copyTrack(track))
		}
	}
	for _, job := range p.jobs {
		state.Resolving = append(state.Resolving, job.progress)
	}
	sort.Slice(state.Resolving, func(i, j int) bool { return state.Resolving[i].ID < state.Resolving[j].ID })
	return state
}

// Clear cancels pending collection work and removes all upcoming positions.
// Playback of the current track is independent of collection resolution.
func (p *GuildPlayer) Clear() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	n := len(p.queue)
	p.cancelJobsLocked()
	p.generation++
	p.queue = nil
	p.halted, p.failures = false, 0
	return n
}

func (p *GuildPlayer) Length() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// MaxSkipTracks includes the active track plus a full queue.
const MaxSkipTracks = MaxQueueTracks + 1

type SkipResult struct {
	Skipped     int
	Remaining   int
	Next        *media.Track
	NextPending bool
}

// SkipN advances N logical positions in one critical section. An already
// cancelled track is removed from logical state immediately, so concurrent
// skips count queued successors rather than counting the same track twice.
// The single run goroutine finishes teardown before starting any successor.
func (p *GuildPlayer) SkipN(count int) (SkipResult, error) {
	if count < 1 || count > MaxSkipTracks {
		return SkipResult{}, fmt.Errorf("Skip count must be between 1 and %d.", MaxSkipTracks)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	result := SkipResult{}
	if p.current != nil {
		result.Skipped++
		p.cancelTrackLocked()
	}
	discard := min(count-result.Skipped, len(p.queue))
	for _, entry := range p.queue[:discard] {
		p.discardEntryLocked(entry)
	}
	clear(p.queue[:discard])
	p.queue = p.queue[discard:]
	result.Skipped += discard
	result.Remaining = len(p.queue)
	if len(p.queue) > 0 {
		next := copyTrack(p.queue[0].Track)
		result.NextPending = p.queue[0].Pending
		result.Next = &next
	}
	p.signal()
	return result, nil
}

// Called only under mu. trackDone stays owned by run until teardown finishes.
func (p *GuildPlayer) cancelTrackLocked(reason ...string) {
	why := "skip"
	if len(reason) > 0 {
		why = reason[0]
	}
	if p.trackCause != nil {
		p.trackCause(errors.New(why))
	}
	if p.trackCancel != nil {
		p.trackCancel()
		p.trackCancel = nil
	}
	p.current = nil
	p.pause = nil
}
func copyTrack(t media.Track) media.Track { return media.CloneTrack(t) }
