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
