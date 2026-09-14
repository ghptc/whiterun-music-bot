package media

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Timing follows a request into the queue. Immutable request fields and a
// synchronized send marker allow concurrent playback and interaction events.
type Timing struct {
	log         *slog.Logger
	start       time.Time
	discordSend sync.Once
}

type timingKey struct{}

func NewTiming(log *slog.Logger) *Timing { return &Timing{log: log, start: time.Now()} }
func WithTiming(ctx context.Context, t *Timing) context.Context {
	return context.WithValue(ctx, timingKey{}, t)
}
func TimingFrom(ctx context.Context) *Timing {
	t, _ := ctx.Value(timingKey{}).(*Timing)
	return t
}

// Event reports stage duration and request age separately (queued tracks can
// wait minutes before playback). A nil receiver supports callers without tracing.
func (t *Timing) Event(name string, start time.Time, attrs ...any) {
	if t == nil {
		return
	}
	fields := []any{"elapsed_ms", float64(time.Since(start).Microseconds()) / 1000, "request_elapsed_ms", float64(time.Since(t.start).Microseconds()) / 1000}
	t.log.Info(name, append(fields, attrs...)...)
}

// FirstDiscordSend records successful UDP submission, not proof of audibility.
func (t *Timing) FirstDiscordSend() {
	if t != nil {
		t.discordSend.Do(func() { t.Event("first_discord_send", t.start) })
	}
}
