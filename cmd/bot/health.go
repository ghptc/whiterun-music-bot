package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"whiterun/internal/discord"
)

const healthInterval = 5 * time.Second
const unresponsiveLimit = 45 * time.Second

type runtimeHealth struct {
	CheckedAt                time.Time      `json:"checked_at"`
	ProcessAlive             bool           `json:"process_alive"`
	SQLiteAccessible         bool           `json:"sqlite_accessible"`
	SessionRecoveryAvailable bool           `json:"session_recovery_available"`
	Runtime                  discord.Health `json:"runtime"`
}

func healthPath() string {
	if p := os.Getenv("BARD_HEALTH_PATH"); p != "" {
		return p
	}
	return "/tmp/bard-runtime-health.json"
}
func healthcheck() error {
	data, err := os.ReadFile(healthPath())
	if err != nil {
		return err
	}
	var h runtimeHealth
	if err = json.Unmarshal(data, &h); err != nil {
		return err
	}
	age := time.Since(h.CheckedAt)
	if age < 0 || age > 3*healthInterval || !h.ProcessAlive || !h.SQLiteAccessible || !h.Runtime.WorkersResponsive || !h.Runtime.GatewayConnected || !h.SessionRecoveryAvailable {
		return errors.New("runtime degraded or health probe stale")
	}
	return nil
}
func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// A persistent rolling budget bounds watchdog-triggered restarts even when
// Docker's restart policy is enabled. Ordinary network/database outages never
// spend this budget; they remain degraded while their own recovery runs.
func reserveRestart(path string, now time.Time) (bool, error) {
	var attempts []time.Time
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &attempts); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	recent := []time.Time{}
	for _, at := range attempts {
		if now.Sub(at) < 10*time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 3 {
		return false, nil
	}
	return true, writeJSON(path, append(recent, now))
}

type runtimeWatchdog struct {
	unresponsiveSince time.Time
	triggered         bool
}

func (w *runtimeWatchdog) observe(h runtimeHealth) bool {
	now := h.CheckedAt
	stalled := !h.Runtime.WorkersResponsive
	for _, p := range h.Runtime.Players {
		// Startup/resolution, pauses, idle queues and exhausted recoveries have
		// explicit distinct states. The stream cancels locally after 45s without
		// audio; only an operation ignoring that cancellation reaches this fallback.
		if ((p.State == "playing" || p.State == "buffering") && now.Sub(p.LastProgress) > 55*time.Second) || (p.State == "canceling" && now.Sub(p.LastProgress) > 10*time.Second) {
			stalled = true
		}
	}
	if !stalled {
		w.unresponsiveSince = time.Time{}
		w.triggered = false
		return false
	}
	if w.unresponsiveSince.IsZero() {
		w.unresponsiveSince = now
	}
	if w.triggered {
		return false
	}
	if !h.Runtime.WorkersResponsive && now.Sub(w.unresponsiveSince) < unresponsiveLimit {
		return false
	}
	w.triggered = true
	return true
}
func monitorRuntime(ctx context.Context, log *slog.Logger, snapshot func(context.Context) runtimeHealth, restart func(), budgetPath string) {
	timer := time.NewTicker(healthInterval)
	defer timer.Stop()
	watchdog := runtimeWatchdog{}
	lastState := ""
	var pending chan runtimeHealth
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		// Keep at most one probe in flight: a wedged dependency must neither
		// stop supervision nor accumulate goroutines on every tick.
		if pending == nil {
			pending = make(chan runtimeHealth, 1)
			out := pending
			go func() {
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				defer func() {
					if recover() != nil {
						out <- runtimeHealth{CheckedAt: time.Now(), ProcessAlive: true}
					}
				}()
				out <- snapshot(probeCtx)
			}()
		}
		probeTimer := time.NewTimer(2 * time.Second)
		var h runtimeHealth
		select {
		case <-ctx.Done():
			probeTimer.Stop()
			return
		case h = <-pending:
			probeTimer.Stop()
			pending = nil
		case <-probeTimer.C:
			h = runtimeHealth{CheckedAt: time.Now(), ProcessAlive: true}
		}
		state := fmt.Sprintf("%t/%t/%t", h.Runtime.GatewayConnected, h.Runtime.WorkersResponsive, h.SQLiteAccessible)
		if state != lastState {
			log.Info("runtime_health_changed", "gateway_connected", h.Runtime.GatewayConnected, "workers_responsive", h.Runtime.WorkersResponsive, "sqlite_accessible", h.SQLiteAccessible)
			lastState = state
		}
		if err := writeJSON(healthPath(), h); err != nil {
			log.Warn("runtime_health_publish_failed", "error", err)
		}
		if watchdog.observe(h) {
			allowed, err := reserveRestart(budgetPath, h.CheckedAt)
			log.Error("process_supervision_triggered", "component", "runtime", "incident_id", h.CheckedAt.UnixNano(), "cause", "functional_stall", "duration_ms", h.CheckedAt.Sub(watchdog.unresponsiveSince).Milliseconds(), "attempt", 1, "restart_allowed", allowed && err == nil, "error", err)
			if allowed && err == nil {
				restart()
				return
			}
		}
	}
}
