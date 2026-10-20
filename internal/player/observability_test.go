package player

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/media"
)

type eventCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*eventCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *eventCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *eventCapture) WithAttrs(a []slog.Attr) slog.Handler {
	return &captureAttrs{h, append([]slog.Attr{}, a...)}
}
func (h *eventCapture) WithGroup(string) slog.Handler { return h }
func (h *eventCapture) find(event, key string, value any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message != event {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.Any() == value {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
func TestSchedulerDiagnosticDecisions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &eventCapture{}
		p := New(context.Background(), slog.New(h), func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		_ = p.Attach(&fakeVoice{})
		p.mu.Lock()
		p.queue = []queueEntry{{Track: media.Track{Position: 9, SpotifyID: "stable"}, Pending: true}}
		p.signal("test_pending")
		p.mu.Unlock()
		synctest.Wait()
		if !h.find("scheduler_wait", "wait_reason", "next_position_pending") {
			t.Error("missing pending reason")
		}
		time.Sleep(schedulerStallAfter + time.Second)
		synctest.Wait()
		if !h.find("scheduler_stall_detected", "expected_event", "resolver_window_wakeup_then_commit") {
			t.Error("missing stall diagnosis")
		}
		p.Stop()
		synctest.Wait()
		if !h.find("scheduler_wakeup", "wakeup_reason", "stop") {
			t.Error("missing stop wakeup")
		}
		h.mu.Lock()
		n := len(h.records)
		h.mu.Unlock()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		h.mu.Lock()
		after := len(h.records)
		h.mu.Unlock()
		if after != n {
			t.Error("idle heartbeat flood")
		}
	})
}
func TestSchedulerBlockingConditions(t *testing.T) {
	p := &GuildPlayer{ctx: context.Background()}
	for _, tc := range []struct {
		reason string
		setup  func()
	}{
		{"voice_detached", func() { p.voice = nil }},
		{"queue_empty", func() { p.voice = &fakeVoice{} }},
		{"provider_blocked", func() { p.providerBlocked = true }},
		{"halted", func() { p.providerBlocked = false; p.halted = true }},
	} {
		tc.setup()
		got, _ := p.schedulerReasonLocked()
		if got != tc.reason {
			t.Errorf("%s: %s", tc.reason, got)
		}
	}
}
func TestDiagnosticAdmissionDoesNotWaitForHandler(t *testing.T) {
	// A saturated admission channel models a writer blocked in any downstream sink.
	p := &GuildPlayer{diagnostics: &diagnostics{events: make(chan slog.Record, 1)}}
	for range 10000 {
		p.diagnosticLocked(slog.LevelInfo, "test", "position", 1)
	}
	if p.diagnostics.dropped.Load() != 9999 {
		t.Fatal("diagnostics did not drop on saturation")
	}
}

func TestEOFReadyAndPendingDiagnostics(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "pending"}[pending], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := &eventCapture{}
				eof := make(chan struct{})
				entered := make(chan struct{})
				p := New(context.Background(), slog.New(h), func(ctx context.Context, track media.Track, _ Voice) error {
					if track.Position == 1 {
						close(entered)
						select {
						case <-eof:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					<-ctx.Done()
					return ctx.Err()
				})
				defer p.Close(context.Background())
				_ = p.Attach(&fakeVoice{})
				_ = p.EnqueueMany(numberedTracks(1, 2), p.Ticket())
				<-entered
				p.mu.Lock()
				p.queue[0].Pending = pending
				p.mu.Unlock()
				close(eof)
				synctest.Wait()
				if pending {
					if !h.find("scheduler_wait", "wait_reason", "next_position_pending") {
						t.Error("missing EOF wait")
					}
				} else if !h.find("scheduler_advance", "position", int64(2)) {
					t.Error("missing successor advance")
				}
			})
		})
	}
}
func TestResolutionCommitAndStaleGenerationDiagnostics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &eventCapture{}
		p := New(context.Background(), slog.New(h), func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		_ = p.Attach(&fakeVoice{})
		ticket := p.Ticket()
		j := begin(t, p, context.Background(), numberedTracks(1, 14), func(_ context.Context, track media.Track, _ bool) (media.Track, error) { return matched(track), nil })
		firstResult(t, j)
		synctest.Wait()
		if err := p.Shuffle(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		p.Clear()
		_ = p.Enqueue(media.Track{}, ticket)
		synctest.Wait()
		if !h.find("resolution_position_commit", "playback_eligible", true) {
			t.Error("missing eligible commit")
		}
		if !h.find("queue_generation_rejected", "requested_generation", ticket) {
			t.Error("missing stale generation")
		}
		if !h.find("shuffle_completed", "queue_size", int64(13)) {
			t.Error("missing shuffle")
		}
	})
}
func TestPauseAndProviderDoNotReportStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &eventCapture{}
		p := New(context.Background(), slog.New(h), func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		defer p.Close(context.Background())
		_ = p.Attach(&fakeVoice{})
		_ = p.Enqueue(media.Track{Position: 1}, p.Ticket())
		synctest.Wait()
		_, _ = p.SetPaused(true)
		time.Sleep(2 * schedulerStallAfter)
		synctest.Wait()
		if !h.find("playback_pause_changed", "paused", true) {
			t.Error("missing pause")
		}
		p.Stop()
		p.mu.Lock()
		p.providerBlocked = true
		p.signal("provider_blocked")
		p.mu.Unlock()
		synctest.Wait()
		time.Sleep(2 * schedulerStallAfter)
		synctest.Wait()
		if !h.find("scheduler_wait", "wait_reason", "provider_blocked") {
			t.Error("missing provider wait")
		}
		if h.find("scheduler_stall_detected", "provider_blocked", true) {
			t.Error("legitimate provider block reported as stall")
		}
	})
}

func TestStallObserverDoesNotAdvanceWithoutNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &eventCapture{}
		started := make(chan struct{}, 1)
		p := New(context.Background(), slog.New(h), func(ctx context.Context, _ media.Track, _ Voice) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
		defer p.Close(context.Background())
		_ = p.Attach(&fakeVoice{})
		p.mu.Lock()
		p.queue = []queueEntry{{Pending: true}}
		p.signal("pending")
		p.mu.Unlock()
		synctest.Wait()
		p.mu.Lock()
		p.queue[0].Pending = false
		p.mu.Unlock() // model lost notification
		time.Sleep(schedulerStallAfter + time.Second)
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("diagnostic timer advanced playback")
		default:
		}
		if !h.find("scheduler_stall_detected", "wait_reason", "notification_missing") {
			t.Fatal("missing notification diagnosis")
		}
		p.mu.Lock()
		p.signal("resolution_commit")
		p.mu.Unlock()
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("explicit notification did not advance")
		}
	})
}

type captureAttrs struct {
	root  *eventCapture
	attrs []slog.Attr
}

func (*captureAttrs) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureAttrs) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(h.attrs...)
	return h.root.Handle(ctx, r)
}
func (h *captureAttrs) WithAttrs(a []slog.Attr) slog.Handler {
	return &captureAttrs{h.root, append(append([]slog.Attr{}, h.attrs...), a...)}
}
func (h *captureAttrs) WithGroup(string) slog.Handler { return h }
func TestPlaybackAttemptCorrelation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &eventCapture{}
		p := New(context.Background(), slog.New(h), func(ctx context.Context, track media.Track, v Voice) error {
			track.Timing.Event("probe_stream", time.Now())
			return v.WriteFrame(ctx, []byte{1})
		})
		defer p.Close(context.Background())
		_ = p.Attach(&fakeVoice{})
		_ = p.Enqueue(media.Track{Position: 17, SpotifyID: "stable"}, p.Ticket())
		synctest.Wait()
		attempt := p.sessionID + "/1"
		for _, event := range []string{"probe_stream", "track_playback_start", "track_playback_end", "playback_attempt_terminal", "player_transition"} {
			if !h.find(event, "playback_attempt_id", attempt) {
				t.Errorf("%s lost attempt correlation", event)
			}
		}
		if !h.find("playback_attempt_terminal", "state", "FINISHED") {
			t.Error("terminal state must be persistable string")
		}
	})
}
func TestConcurrentDiagnosticPressure(t *testing.T) {
	p := &GuildPlayer{diagnostics: &diagnostics{events: make(chan slog.Record, 1)}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				p.mu.Lock()
				p.diagnosticLocked(slog.LevelInfo, "pressure", "position", 1)
				p.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if p.diagnostics.dropped.Load() != 7999 {
		t.Fatal("unexpected admission accounting")
	}
}

type blockingDiagnosticHandler struct {
	entered, release chan struct{}
	once             sync.Once
}

func (*blockingDiagnosticHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockingDiagnosticHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "scheduler_wait" {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return nil
}
func (h *blockingDiagnosticHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockingDiagnosticHandler) WithGroup(string) slog.Handler      { return h }
func TestBlockedDiagnosticSinkDoesNotBlockPlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &blockingDiagnosticHandler{entered: make(chan struct{}), release: make(chan struct{})}
		started := make(chan struct{})
		p := New(context.Background(), slog.New(h), func(ctx context.Context, _ media.Track, _ Voice) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		defer p.Close(context.Background())
		defer close(h.release)
		<-h.entered
		_ = p.Attach(&fakeVoice{})
		_ = p.Enqueue(media.Track{}, p.Ticket())
		<-started
		p.Stop()
	})
}
