//go:build soak

package player

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"testing"
	"time"
	"whiterun/internal/media"
)

// Opt-in local workload; it does not claim real Discord/network/FFmpeg soak.
func TestQualitySoak(t *testing.T) {
	duration := 30 * time.Minute
	if raw := os.Getenv("BARD_QA_SOAK_DURATION"); raw != "" {
		var err error
		duration, err = time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			t.Fatalf("invalid BARD_QA_SOAK_DURATION %q", raw)
		}
	}
	started := make(chan string, 16)
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, tr media.Track, _ Voice) error {
		started <- tr.Title
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.Close(context.Background())
	if err := p.Attach(&fakeVoice{}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutines := runtime.NumGoroutine()
	fdsBefore, _ := os.ReadDir("/proc/self/fd")
	startedAt := time.Now()
	cycles := 0
	for time.Since(startedAt) < duration {
		tracks := make([]media.Track, 500)
		for i := range tracks {
			tracks[i] = media.Track{Title: fmt.Sprintf("%d/%d", cycles, i), Artists: []string{"artist"}, RequestedBy: "soak"}
		}
		if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
			t.Fatal(err)
		}
		receive(t, started, tracks[0].Title)
		if _, err := p.SetPaused(true); err != nil {
			t.Fatal(err)
		}
		if err := p.Replay(); err != nil {
			t.Fatal(err)
		}
		receive(t, started, tracks[0].Title)
		if p.State().Paused {
			t.Fatal("replay retained pause")
		}
		if _, err := p.SkipN(1); err != nil {
			t.Fatal(err)
		}
		receive(t, started, tracks[1].Title)
		if err := p.Shuffle(); err != nil {
			t.Fatal(err)
		}
		if p.Length() != 498 {
			t.Fatal("lost or duplicated queue entries")
		}
		p.Stop()
		if state := p.State(); state.Current != nil || len(state.Entries) != 0 {
			t.Fatal("stop retained queue")
		}
		cycles++
	}
	p.Close(context.Background())
	runtime.GC()
	runtime.ReadMemStats(&after)
	fdsAfter, _ := os.ReadDir("/proc/self/fd")
	t.Logf("cycles=%d elapsed=%s heap_before=%d heap_after=%d total_alloc_delta=%d goroutines_before=%d goroutines_after=%d fd_before=%d fd_after=%d",
		cycles, time.Since(startedAt), before.HeapAlloc, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc, goroutines, runtime.NumGoroutine(), len(fdsBefore), len(fdsAfter))
	// Exact ownership assertion: Close must have joined this player's run loop.
	select {
	case <-p.done:
	default:
		t.Fatal("player goroutine survived Close")
	}
}
