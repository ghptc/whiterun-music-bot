package player

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
	"whiterun/internal/media"
)

// Real monotonic clock; frame-only in-memory Voice. No provider or Discord SLA.
func BenchmarkQA013EOFTransition(b *testing.B) {
	started, finish := make(chan struct{}), make(chan struct{})
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		started <- struct{}{}
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer p.Close(context.Background())
	if err := p.Attach(&fakeVoice{}); err != nil {
		b.Fatal(err)
	}
	var transition, firstFrame time.Duration
	b.ReportAllocs()
	for b.Loop() {
		at := time.Now()
		if err := p.EnqueueMany([]media.Track{{Title: "first"}, {Title: "next"}}, p.Ticket()); err != nil {
			b.Fatal(err)
		}
		<-started
		firstFrame += time.Since(at)
		at = time.Now()
		finish <- struct{}{}
		<-started
		transition += time.Since(at)
		p.Stop()
	}
	b.ReportMetric(float64(transition.Nanoseconds())/float64(b.N), "transition_ns/op")
	b.ReportMetric(float64(firstFrame.Nanoseconds())/float64(b.N), "first_frame_ns/op")
}
