package media

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func optAdmission(t *testing.T, concurrent int, interval time.Duration) *ProviderAdmission {
	t.Helper()
	g, err := NewProviderAdmission(ProviderAdmissionConfig{Concurrency: concurrent, Interval: interval})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func TestOPT026AdmissionRateAndConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := optAdmission(t, 2, 0)
		hold := make(chan struct{})
		var active, maximum, started atomic.Int32
		for range 5 {
			go func() {
				release, _, err := g.Admit(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				n := active.Add(1)
				for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
				}
				started.Add(1)
				<-hold
				active.Add(-1)
				release()
				release()
			}()
		}
		synctest.Wait()
		if active.Load() != 2 || started.Load() != 2 {
			t.Fatal(active.Load(), started.Load())
		}
		close(hold)
		synctest.Wait()
		if maximum.Load() != 2 || started.Load() != 5 {
			t.Fatal(maximum.Load(), started.Load())
		}
		rate := optAdmission(t, 4, 100*time.Millisecond)
		start := time.Now()
		for range 5 {
			release, _, err := rate.Admit(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			release()
		}
		if time.Since(start) != 400*time.Millisecond {
			t.Fatal("admission interval bypassed", time.Since(start))
		}
	})
}
func TestOPT026OneGlobalTrialAndFiniteBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := optAdmission(t, 4, 0)
		failure := &ExtractionError{Category: "youtube_bot_challenge"}
		g.Observe(failure)
		if _, _, err := g.Admit(context.Background()); !errors.Is(err, ErrProviderAdmissionBlocked) {
			t.Fatal(err)
		}
		for attempt, delay := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute} {
			ctx, cancel := context.WithCancel(context.Background())
			permits := make(chan string, 2)
			for range 2 {
				go func() {
					permit, err := g.WaitRecovery(ctx)
					if err == nil {
						permits <- permit
					}
				}()
			}
			synctest.Wait()
			time.Sleep(delay)
			synctest.Wait()
			if len(permits) != 1 {
				t.Fatal("multiple guild trials admitted", len(permits))
			}
			permit := <-permits
			release, _, err := g.Admit(WithProviderPermit(context.Background(), permit))
			if err != nil {
				t.Fatal(err)
			}
			release()
			if _, _, err := g.Admit(context.Background()); !errors.Is(err, ErrProviderAdmissionBlocked) {
				t.Fatal("unlicensed operation during half-open", err)
			}
			g.Observe(failure)
			g.FinishTrial(permit, failure)
			cancel()
			synctest.Wait()
			if g.Snapshot().Attempts != attempt+1 {
				t.Fatal(g.Snapshot())
			}
		}
		if !g.Snapshot().Exhausted {
			t.Fatal(g.Snapshot())
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if _, err := g.WaitRecovery(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if g.Snapshot().Attempts != 3 {
			t.Fatal("budget silently renewed")
		}
		g.Rearm()
		if g.Snapshot().Blocked {
			t.Fatal("explicit operator rearm failed")
		}
	})
}
func TestOPT026RecoveryRequiresNewMediaEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := optAdmission(t, 4, 0)
		oldRelease, oldEpoch, err := g.Admit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		oldRelease()
		failure := &ExtractionError{Category: "youtube_bot_challenge"}
		g.Observe(failure)
		g.SustainedMedia(context.Background(), oldEpoch)
		if !g.Snapshot().Blocked {
			t.Fatal("existing healthy stream reset incident")
		}
		time.Sleep(15 * time.Second)
		permit, err := g.WaitRecovery(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		trialCtx := WithProviderPermit(context.Background(), permit)
		release, _, err := g.Admit(trialCtx)
		if err != nil {
			t.Fatal(err)
		}
		release()
		g.FinishTrial(permit, nil)
		if g.Snapshot().Blocked || g.Snapshot().Attempts != 1 {
			t.Fatal(g.Snapshot())
		}
		// Metadata success cannot mint a new budget if media still rejects access.
		g.Observe(failure)
		if g.Snapshot().Attempts != 1 {
			t.Fatal("budget reset after metadata-only success")
		}
		time.Sleep(30 * time.Second)
		permit, err = g.WaitRecovery(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		trialCtx = WithProviderPermit(context.Background(), permit)
		release, epoch, err := g.Admit(trialCtx)
		if err != nil {
			t.Fatal(err)
		}
		release()
		g.SustainedMedia(trialCtx, epoch)
		if s := g.Snapshot(); s.Blocked || s.Incident != "" || s.Attempts != 0 {
			t.Fatal(s)
		}
	})
}
func TestOPT026LocalFailuresNeverOpenGlobalBreaker(t *testing.T) {
	for _, err := range []error{&ExtractionError{Category: "youtube_extractor_failure"}, &ExtractionError{Category: "youtube_network_timeout"}, &ExtractionError{Category: "youtube_auth_required", VideoSpecific: true}, errors.New("HTTP Error 403"), context.Canceled} {
		g := optAdmission(t, 1, 0)
		g.Observe(err)
		if g.Snapshot().Blocked {
			t.Fatal("local/ambiguous failure treated as global", err)
		}
	}
}
func TestOPT026BlockedAdmissionWakesRateWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := optAdmission(t, 1, time.Hour)
		release, _, err := g.Admit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		release()
		done := make(chan error, 1)
		go func() { _, _, err := g.Admit(context.Background()); done <- err }()
		synctest.Wait()
		g.Observe(&ExtractionError{Category: "youtube_rate_limited"})
		synctest.Wait()
		if err := <-done; !errors.Is(err, ErrProviderAdmissionBlocked) {
			t.Fatal(err)
		}
		if len(g.slots) != 0 {
			t.Fatal("admission capacity leaked")
		}
	})
}
