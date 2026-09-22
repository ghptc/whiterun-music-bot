package player

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"whiterun/internal/media"
)

// Voice is the external transport boundary; tests use an in-memory transport.
type Voice interface {
	WriteFrame(context.Context, []byte) error
	Speaking(context.Context, bool) error
	Close(context.Context)
}
type PlayFunc func(context.Context, media.Track, Voice) error

const MaxQueueTracks = 1000

type GuildPlayer struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	wake        chan struct{}
	log         *slog.Logger
	play        PlayFunc
	voice       Voice
	current     *media.Track
	queue       []media.Track
	trackCancel context.CancelFunc
	trackDone   chan struct{}
	generation  uint64
}

func New(ctx context.Context, log *slog.Logger, play PlayFunc) *GuildPlayer {
	ctx, cancel := context.WithCancel(ctx)
	p := &GuildPlayer{ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), log: log, play: play}
	go p.run()
	return p
}
func (p *GuildPlayer) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *GuildPlayer) Attach(v Voice) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return errors.New("The Bard is shutting down.")
	}
	if p.voice != nil {
		return errors.New("The Bard is already summoned.")
	}
	p.voice = v
	p.generation++
	p.signal()
	return nil
}
func (p *GuildPlayer) Ticket() uint64 { p.mu.Lock(); defer p.mu.Unlock(); return p.generation }
func (p *GuildPlayer) Enqueue(t media.Track, ticket uint64) error {
	return p.EnqueueMany([]media.Track{t}, ticket)
}

// EnqueueMany appends a collection atomically; concurrent commands cannot interleave it.
func (p *GuildPlayer) EnqueueMany(tracks []media.Track, ticket uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil || p.voice == nil || ticket != p.generation {
		return errors.New("Playback changed while searching. Ask the Bard again.")
	}
	if len(tracks) > MaxQueueTracks-len(p.queue) {
		return errors.New("The collection will not fit in the queue (1000 upcoming tracks maximum).")
	}
	p.queue = append(p.queue, tracks...)
	p.signal()
	return nil
}
func (p *GuildPlayer) Skip() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trackCancel == nil {
		return false
	}
	p.trackCancel()
	return true
}
func (p *GuildPlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = nil
	p.generation++
	if p.trackCancel != nil {
		p.trackCancel()
	}
}

// Leave detaches first, so in-flight searches cannot resurrect playback.
// Discord orchestration serializes Leave and Attach for each guild.
func (p *GuildPlayer) Leave(ctx context.Context) {
	p.mu.Lock()
	v := p.voice
	p.voice = nil
	p.queue = nil
	p.generation++
	if p.trackCancel != nil {
		p.trackCancel()
	}
	done := p.trackDone
	p.mu.Unlock()
	if done != nil {
		<-done
	}
	if v != nil {
		v.Close(ctx)
	}
}
func (p *GuildPlayer) Close(ctx context.Context) { p.cancel(); p.Leave(ctx); <-p.done }
func (p *GuildPlayer) run() {
	defer close(p.done)
	for {
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if p.voice == nil || len(p.queue) == 0 {
			p.mu.Unlock()
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		t := p.queue[0]
		p.queue[0] = media.Track{}
		p.queue = p.queue[1:]
		ctx, cancel := context.WithCancel(p.ctx)
		done := make(chan struct{})
		p.trackDone = done
		p.trackCancel = cancel
		p.current = &t
		v := p.voice
		p.mu.Unlock()
		err := p.play(ctx, t, v)
		if err != nil && ctx.Err() == nil {
			p.log.Warn("track failed; advancing queue", "title", t.Title, "error", err)
		}
		cancel()
		p.mu.Lock()
		p.current = nil
		p.trackCancel = nil
		p.trackDone = nil
		close(done)
		p.mu.Unlock()
	}
}
