package player

import (
	"errors"
	"whiterun/internal/media"
)

// Previous replays the last actually started recording from its beginning.
// The interrupted track returns immediately after it; transient resources are
// always torn down by the single playback loop before either track starts.
func (p *GuildPlayer) Previous() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	n := len(p.history)
	if p.current != nil && p.startedSequence == p.sequence {
		n--
	}
	if n == 0 || p.ctx.Err() != nil || p.voice == nil {
		return errors.New("No previous song is available.")
	}
	needed := 1
	if p.current != nil {
		needed++
	}
	if len(p.queue)+needed > MaxQueueTracks {
		return errors.New("The queue is full.")
	}
	previous := copyTrack(p.history[n-1])
	prefix := []queueEntry{{Track: previous}}
	if p.current != nil {
		prefix = append(prefix, queueEntry{Track: copyTrack(*p.current)})
	}
	p.history = p.history[:n-1]
	p.cancelTrackLocked("previous")
	p.queue = append(prefix, p.queue...)
	p.halted = false
	p.failures = 0
	p.signal()
	return nil
}

func (p *GuildPlayer) History() []media.Track {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]media.Track, len(p.history))
	for i, t := range p.history {
		result[i] = copyTrack(t)
	}
	return result
}
