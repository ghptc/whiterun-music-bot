package player

import "whiterun/internal/media"

// Snapshot returns copies, never exposing mutable queue state.
func (p *GuildPlayer) Snapshot() (*media.Track, []media.Track) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var current *media.Track
	if p.current != nil {
		t := *p.current
		current = &t
	}
	return current, append([]media.Track(nil), p.queue...)
}

// Clear removes upcoming tracks without cancelling the current track or searches.
func (p *GuildPlayer) Clear() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.queue)
	p.queue = nil
	return n
}

func (p *GuildPlayer) Length() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}
