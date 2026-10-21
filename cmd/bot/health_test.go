package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"whiterun/internal/discord"
	"whiterun/internal/player"
)

func TestREL024HealthcheckAndPersistentRestartBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	t.Setenv("BARD_HEALTH_PATH", path)
	h := runtimeHealth{CheckedAt: time.Now(), ProcessAlive: true, SQLiteAccessible: true, SessionRecoveryAvailable: true, Runtime: discord.Health{GatewayConnected: true, WorkersResponsive: true}}
	if err := writeJSON(path, h); err != nil {
		t.Fatal(err)
	}
	if err := healthcheck(); err != nil {
		t.Fatal(err)
	}
	h.CheckedAt = time.Now().Add(-time.Minute)
	if err := writeJSON(path, h); err != nil {
		t.Fatal(err)
	}
	if healthcheck() == nil {
		t.Fatal("stale health accepted")
	}
	budget := filepath.Join(t.TempDir(), "budget.json")
	now := time.Now()
	for i := 0; i < 4; i++ {
		ok, err := reserveRestart(budget, now)
		if err != nil || ok != (i < 3) {
			t.Fatal(i, ok, err)
		}
	}
	ok, err := reserveRestart(budget, now.Add(11*time.Minute))
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err = os.WriteFile(budget, []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := reserveRestart(budget, now); ok {
		t.Fatal("corrupt budget must fail closed")
	}
}
func TestREL024WatchdogSeparatesDegradationAndStalls(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"paused", "queue_empty", "voice_suspended", "provider_blocked", "voice_detached"} {
		w := runtimeWatchdog{}
		h := runtimeHealth{CheckedAt: now, Runtime: discord.Health{WorkersResponsive: true, Players: map[string]player.Health{"1": {State: state, LastProgress: now.Add(-time.Hour)}}}}
		for range 15 {
			if w.observe(h) {
				t.Fatal("legitimate wait restarted", state)
			}
			h.CheckedAt = h.CheckedAt.Add(time.Minute)
		}
	}
	w := runtimeWatchdog{}
	h := runtimeHealth{CheckedAt: now, Runtime: discord.Health{WorkersResponsive: false}}
	if w.observe(h) {
		t.Fatal("immediate restart")
	}
	h.CheckedAt = now.Add(45 * time.Second)
	if !w.observe(h) || w.observe(h) {
		t.Fatal("stall must request exactly one restart")
	}
	t.Log("lock stall: detection/restart request=45s (virtual clock), limit=60s")
	w = runtimeWatchdog{}
	h.Runtime = discord.Health{WorkersResponsive: true, Players: map[string]player.Health{"1": {State: "playing", LastProgress: now.Add(-56 * time.Second)}}}
	if !w.observe(h) {
		t.Fatal("audio operation ignoring cancellation not supervised")
	}
}
func TestREL024BlockedProbeIsSupervisedWithoutGoroutineFlood(t *testing.T) {
	// Run the actual monitor with a blocked probe, real health-file publication
	// and real persistent restart admission; only the final os.Exit is captured.
	t.Setenv("BARD_HEALTH_PATH", filepath.Join(t.TempDir(), "health.json"))
	budget := filepath.Join(t.TempDir(), "budget.json")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		var calls, restarts atomic.Int32
		done := make(chan struct{})
		started := time.Now()
		go func() {
			defer close(done)
			monitorRuntime(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(context.Context) runtimeHealth { calls.Add(1); <-release; return runtimeHealth{} }, func() { restarts.Add(1) }, budget)
		}()
		time.Sleep(time.Minute)
		close(release)
		<-done
		if calls.Load() != 1 || restarts.Load() != 1 {
			t.Fatal(calls.Load(), restarts.Load())
		}
		data, err := os.ReadFile(budget)
		if err != nil {
			t.Fatal(err)
		}
		var at []time.Time
		if err = json.Unmarshal(data, &at); err != nil || len(at) != 1 {
			t.Fatal(string(data), err)
		}
		detection := at[0].Sub(started)
		if detection > 60*time.Second {
			t.Fatal(detection)
		}
		t.Logf("blocked probe restart admitted after %s; one probe, one restart", detection)
	})
}
