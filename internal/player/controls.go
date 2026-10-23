package player

import (
	"errors"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Positions include unresolved collection entries. Move whole entries so their
// job IDs and original collection positions continue to identify resolver work.
func (p *GuildPlayer) Remove(position int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if position < 1 || position > len(p.queue) {
		return errors.New("Choose an upcoming queue position.")
	}
	i := position - 1
	entry := p.queue[i]
	p.discardEntryLocked(entry)
	p.mutationLocked("remove", "collection_id", entry.jobID, "position", entry.Track.Position, "queue_position", position)
	p.queue = append(p.queue[:i], p.queue[i+1:]...)
	p.persistLocked()
	p.signal("remove")
	return nil
}
func (p *GuildPlayer) Move(from, to int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if from < 1 || to < 1 || from > len(p.queue) || to > len(p.queue) {
		return errors.New("Choose upcoming queue positions.")
	}
	i, j := from-1, to-1
	entry := p.queue[i]
	if i < j {
		copy(p.queue[i:j], p.queue[i+1:j+1])
	} else {
		copy(p.queue[j+1:i+1], p.queue[j:i])
	}
	p.queue[j] = entry
	p.mutationLocked("move", "collection_id", entry.jobID, "position", entry.Track.Position, "from_queue_position", from, "to_queue_position", to)
	p.persistLocked()
	p.signal("move")
	return nil
}
func (p *GuildPlayer) Shuffle() error { return p.shuffle(rand.Shuffle) }

func (p *GuildPlayer) shuffle(shuffle func(int, func(int, int))) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return errors.New("The queue is empty.")
	}
	p.diagnosticLocked(slog.LevelInfo, "shuffle_started", "queue_size", len(p.queue))
	shuffle(len(p.queue), func(i, j int) { p.queue[i], p.queue[j] = p.queue[j], p.queue[i] })
	p.mutationLocked("shuffle")
	p.diagnosticLocked(slog.LevelInfo, "shuffle_completed", "queue_size", len(p.queue))
	p.persistLocked()
	p.signal("shuffle")
	return nil
}
func (p *GuildPlayer) Jump(position int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if position < 1 || position > len(p.queue) {
		return errors.New("Choose an upcoming queue position.")
	}
	p.mutationLocked("jump", "queue_position", position)
	p.cancelTrackLocked("jump")
	for _, entry := range p.queue[:position-1] {
		p.discardEntryLocked(entry)
	}
	clear(p.queue[:position-1])
	p.queue = p.queue[position-1:]
	p.persistLocked()
	p.signal("jump")
	return nil
}
func (p *GuildPlayer) Replay() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return errors.New("The Bard is not playing.")
	}
	if len(p.queue) >= MaxQueueTracks {
		return errors.New("The queue is full.")
	}
	t := copyTrack(*p.current)
	t.ResumeOffset = 0
	p.cancelTrackLocked("replay")
	p.queue = append([]queueEntry{{Track: t}}, p.queue...)
	p.persistLocked()
	p.signal()
	return nil
}
func (p *GuildPlayer) SetLoop(mode string) error {
	if mode != "off" && mode != "track" && mode != "queue" {
		return errors.New("Loop mode must be off, track, or queue.")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loop = mode
	p.persistLocked()
	return nil
}

// Autoplay records intent, but never pretends an unavailable provider is active.
func (p *GuildPlayer) SetAutoplay(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.autoplay = enabled
	p.persistLocked()
}
func ParseTimestamp(raw string) (time.Duration, error) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errors.New("Use MM:SS or HH:MM:SS.")
	}
	total := int64(0)
	for i, s := range parts {
		if s == "" || len(s) > 6 {
			return 0, errors.New("Invalid timestamp.")
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, errors.New("Invalid timestamp.")
			}
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || (i > 0 && n > 59) {
			return 0, errors.New("Invalid timestamp.")
		}
		total = total*60 + n
	}
	if total > int64((24*time.Hour)/time.Second) {
		return 0, errors.New("Timestamp is too large.")
	}
	return time.Duration(total) * time.Second, nil
}
func (p *GuildPlayer) Seek(raw string) error {
	position, err := ParseTimestamp(raw)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current == nil {
		return errors.New("The Bard is not playing.")
	}
	if p.current.Duration > 0 && position > p.current.Duration {
		return errors.New("Position exceeds the song duration.")
	}
	return errors.New("Seek is unavailable with the current audio pipeline. Playback has not changed.")
}
