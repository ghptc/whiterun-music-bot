package player

import (
	"sync/atomic"
	"time"
)

// Health separates a responsive worker from an idle or intentionally suspended
// player. It never waits for a mutex owned by a stalled worker.
type Health struct {
	Responsive        bool      `json:"responsive"`
	State             string    `json:"state"`
	Pending           int       `json:"pending"`
	LastProgress      time.Time `json:"last_progress,omitempty"`
	RecoveryExhausted bool      `json:"recovery_exhausted"`
}

type playbackProgress struct{ at atomic.Int64 }

func (p *GuildPlayer) Health() Health {
	if !p.mu.TryLock() {
		return Health{State: "lock_unresponsive"}
	}
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return Health{State: "worker_stopped"}
	default:
	}
	reason, _ := p.schedulerReasonLocked()
	h := Health{Responsive: true, State: reason, Pending: len(p.queue), RecoveryExhausted: (p.voiceBlocked && p.voiceAttempts >= 5) || (p.halted && p.networkAttempts >= 3) || (p.providerBlocked && p.providerRetries >= providerAutomaticRetries && !p.providerProbe)}
	if p.providerGlobal && p.admission != nil {
		h.RecoveryExhausted = p.admission.Snapshot().Exhausted
	}
	if p.current != nil {
		h.State = "buffering"
		if p.startedSequence == p.sequence {
			h.State = "playing"
		}
		if p.pause.paused() {
			h.State = "paused"
			p.progress.at.Store(time.Now().UnixNano())
		}
		h.LastProgress = time.Unix(0, p.progress.at.Load())
	}
	if p.current == nil && p.trackDone != nil {
		h.State = "canceling"
		h.LastProgress = p.canceledAt
	}
	return h
}
