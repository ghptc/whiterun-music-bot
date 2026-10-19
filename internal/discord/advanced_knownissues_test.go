//go:build qa008_knownissues

package discord

import (
	"context"
	"github.com/disgoorg/disgo/rest"
	"testing"
	"testing/synctest"
)

// QA008-D03: Close returns but a maintenance goroutine survives. synctest
// reports a deterministic deadlock when the bubble attempts to finish.
func TestKnownIssuesDisgoLimiterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := rest.NewRateLimiter()
		limiter.Close(context.Background())
	})
}
