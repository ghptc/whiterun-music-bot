package media

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const ProviderRecoveryTrials = 3

// ProviderBackoff is shared with the player: admission owns no retry goroutine.
func ProviderBackoff(attempt int) time.Duration {
	return min(2*time.Minute, 15*time.Second*time.Duration(1<<min(attempt, 3)))
}

var ErrProviderAdmissionBlocked = errors.New("YouTube admission suspended")

type ProviderAdmissionConfig struct {
	Concurrency int
	Interval    time.Duration
}

func DefaultProviderAdmissionConfig() ProviderAdmissionConfig {
	return ProviderAdmissionConfig{Concurrency: 4, Interval: 250 * time.Millisecond}
}

type ProviderStatus struct {
	Blocked, Exhausted bool
	Incident, Category string
	Attempts           int
	RetryAt            time.Time
}

// ProviderAdmission coordinates this process's single YouTube egress. It limits
// starts, not bytes/HTTP subrequests or already healthy long-running streams.
// The existing player/recovery caller performs the trials; no second loop runs.
type ProviderAdmission struct {
	mu                      sync.Mutex
	slots                   chan struct{}
	interval                time.Duration
	nextStart               time.Time
	changed                 chan struct{}
	status                  ProviderStatus
	epoch                   uint64
	probe                   string
	probeSpent              bool
	failures, probeFailures uint64
	identity                string
}

func NewProviderAdmission(c ProviderAdmissionConfig) (*ProviderAdmission, error) {
	if c.Concurrency < 1 || c.Concurrency > 32 || c.Interval < 0 {
		return nil, errors.New("invalid YouTube admission limits")
	}
	return &ProviderAdmission{slots: make(chan struct{}, c.Concurrency), interval: c.Interval, changed: make(chan struct{}), identity: fmt.Sprintf("youtube/%d", time.Now().UnixNano())}, nil
}
func (g *ProviderAdmission) Snapshot() ProviderStatus {
	if g == nil {
		return ProviderStatus{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}
func (g *ProviderAdmission) notifyLocked() { close(g.changed); g.changed = make(chan struct{}) }

// Only confirmed cross-track provider restrictions open the global breaker.
// Timeouts, missing binaries, malformed JSON and an unqualified 403 don't.
func GlobalProviderFailure(err error) string {
	if err == nil || errors.Is(err, ErrProviderAdmissionBlocked) {
		return ""
	}
	var extraction *ExtractionError
	if errors.As(err, &extraction) && extraction.VideoSpecific {
		return ""
	}
	category := ExtractionCategory(err)
	var classified interface{ ProviderCategory() string }
	if category == "" && errors.As(err, &classified) {
		category = classified.ProviderCategory()
	}
	switch category {
	case "youtube_bot_challenge", "youtube_auth_required", "youtube_auth_challenge", "youtube_rate_limited":
		return category
	}
	return ""
}
func (g *ProviderAdmission) Observe(err error) {
	if g == nil {
		return
	}
	category := GlobalProviderFailure(err)
	if category == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status.Incident == "" {
		g.epoch++
		g.status.Incident = fmt.Sprintf("%s/%d", g.identity, g.epoch)
		g.status.Attempts = 0
	}
	g.failures++
	if !g.status.Blocked {
		g.status.RetryAt = time.Now().Add(ProviderBackoff(g.status.Attempts))
	}
	g.status.Blocked = true
	g.status.Category = category
	g.status.Exhausted = g.status.Attempts >= ProviderRecoveryTrials
	g.notifyLocked()
}

type providerPermitKey struct{}

func WithProviderPermit(ctx context.Context, permit string) context.Context {
	return context.WithValue(ctx, providerPermitKey{}, permit)
}
func providerPermit(ctx context.Context) string {
	v, _ := ctx.Value(providerPermitKey{}).(string)
	return v
}

// WaitRecovery is the existing recovery policy, shared rather than per-guild.
// Exhaustion waits for cancellation or explicit process-wide operator rearm;
// guild play/resume/leave never reset this budget.
func (g *ProviderAdmission) WaitRecovery(ctx context.Context) (string, error) {
	if g == nil {
		return "", ctx.Err()
	}
	for {
		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return "", err
		}
		if !g.status.Blocked {
			g.mu.Unlock()
			return "", nil
		}
		delay := time.Until(g.status.RetryAt)
		eligible := g.probe == "" && g.status.Attempts < ProviderRecoveryTrials
		if eligible && delay <= 0 {
			g.status.Attempts++
			g.status.Exhausted = false
			g.probe = fmt.Sprintf("%s/trial/%d", g.status.Incident, g.status.Attempts)
			g.probeSpent = false
			g.probeFailures = g.failures
			result := g.probe
			g.mu.Unlock()
			return result, nil
		}
		changed := g.changed
		g.mu.Unlock()
		var timer *time.Timer
		var tick <-chan time.Time
		if eligible {
			timer = time.NewTimer(max(time.Millisecond, delay))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return "", ctx.Err()
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
func (g *ProviderAdmission) FinishTrial(permit string, err error) {
	if g == nil || permit == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.probe != permit {
		return
	}
	if err == nil && g.failures == g.probeFailures {
		g.status.Blocked = false
		g.status.Exhausted = false
	} else {
		// Cancellation before any external admission doesn't spend a trial.
		if errors.Is(err, context.Canceled) && !g.probeSpent {
			g.status.Attempts--
		}
		g.status.RetryAt = time.Now().Add(ProviderBackoff(g.status.Attempts))
		g.status.Exhausted = g.status.Attempts >= ProviderRecoveryTrials
	}
	g.probe = ""
	g.notifyLocked()
	// Keep the incident/budget through metadata-only success. It is reset only
	// by sustained new media or an explicit operator action, never by /resume.
}

// Rearm is an administrative process-level action, not a Discord guild control.
// Call only after operator approval that access is available again.
func (g *ProviderAdmission) Rearm() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.probe != "" {
		return
	}
	g.epoch++
	g.status = ProviderStatus{}
	g.notifyLocked()
}

// Admit returns an idempotent capacity release and an epoch for new media
// evidence. Call release at the first sent frame for streams, at exit otherwise.
func (g *ProviderAdmission) Admit(ctx context.Context) (func(), uint64, error) {
	if g == nil {
		return func() {}, 0, ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	release := sync.OnceFunc(func() { <-g.slots })
	for {
		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			release()
			return nil, 0, err
		}
		if g.status.Blocked && (g.probe == "" || providerPermit(ctx) != g.probe) {
			category := g.status.Category
			g.mu.Unlock()
			release()
			return nil, 0, &ExtractionError{Category: category, Cause: ErrProviderAdmissionBlocked}
		}
		delay := time.Until(g.nextStart)
		if delay <= 0 {
			g.nextStart = time.Now().Add(g.interval)
			epoch := g.epoch
			if g.probe != "" && providerPermit(ctx) == g.probe {
				g.probeSpent = true
			}
			g.mu.Unlock()
			return release, epoch, nil
		}
		changed := g.changed
		g.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, 0, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// SustainedMedia confirms newly admitted audio, never an old healthy stream.
func (g *ProviderAdmission) SustainedMedia(ctx context.Context, epoch uint64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch != g.epoch || g.status.Incident == "" {
		return
	}
	if g.status.Blocked && (g.probe == "" || providerPermit(ctx) != g.probe || g.failures != g.probeFailures) {
		return
	}
	g.epoch++
	g.status = ProviderStatus{}
	g.probe = ""
	g.notifyLocked()
}
