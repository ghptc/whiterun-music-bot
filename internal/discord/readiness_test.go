package discord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type recoveringVoice struct {
	hold  bool
	ready chan struct{}
}

func (r recoveringVoice) ShouldHoldFrames() bool { return r.hold }
func (r recoveringVoice) WaitReady(ctx context.Context) (time.Duration, error) {
	select {
	case <-r.ready:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func TestVoiceRecoveryWait(t *testing.T) {
	r := recoveringVoice{hold: true, ready: make(chan struct{})}
	time.AfterFunc(30*time.Millisecond, func() { close(r.ready) })
	// Recovery can outlive the old proportional 10ms budget without canceling
	// the track; the timeout belongs only to the readiness operation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := waitVoiceReady(ctx, r, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}
func TestVoiceRecoveryFailureAndCancellation(t *testing.T) {
	r := recoveringVoice{hold: true, ready: make(chan struct{})}
	err := waitVoiceReady(context.Background(), r, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "DAVE readiness") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitVoiceReady(ctx, r, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r.hold = false
	if err := waitVoiceReady(context.Background(), r, time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
