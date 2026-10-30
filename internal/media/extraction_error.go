package media

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ExtractionError retains the machine-readable cause, never provider stderr,
// cookies, URLs or subprocess arguments. Unwrap preserves context cancellation.
type ExtractionError struct {
	Category string
	Cause    error
	ExitCode *int
}

func (e *ExtractionError) Error() string {
	if e.ExitCode != nil {
		return fmt.Sprintf("YouTube extraction: %s (exit_code=%d)", e.Category, *e.ExitCode)
	}
	return "YouTube extraction: " + e.Category
}
func (e *ExtractionError) Unwrap() error { return e.Cause }

func ExtractionCategory(err error) string {
	var e *ExtractionError
	if errors.As(err, &e) {
		return e.Category
	}
	return ""
}
func ProviderUnavailable(err error) bool {
	switch ExtractionCategory(err) {
	case "youtube_bot_challenge", "youtube_auth_required", "youtube_rate_limited", "youtube_network_timeout", "youtube_extractor_failure":
		return true
	}
	return false
}

// Check stderr even on exit 0: ignore-errors/no-formats can mask a rejection.
func ClassifyExtraction(stderr []byte, cause error) error {
	s := strings.ToLower(string(stderr))
	category := ""
	switch {
	case strings.Contains(s, "not a bot"), strings.Contains(s, "bot challenge"):
		category = "youtube_bot_challenge"
	case strings.Contains(s, "sign in"), strings.Contains(s, "log in"), strings.Contains(s, "login required"), strings.Contains(s, "authentication required"), strings.Contains(s, "cookies are required"):
		category = "youtube_auth_required"
	case strings.Contains(s, "http error 429"), strings.Contains(s, "status=429"), strings.Contains(s, "status code 429"), strings.Contains(s, "too many requests"), strings.Contains(s, "rate limit"):
		category = "youtube_rate_limited"
	case errors.Is(cause, context.DeadlineExceeded), strings.Contains(s, "timed out"), strings.Contains(s, "timeout"):
		category = "youtube_network_timeout"
	case strings.Contains(s, "video unavailable"), strings.Contains(s, "private video"), strings.Contains(s, "has been removed"), strings.Contains(s, "not available in your country"):
		category = "youtube_video_unavailable"
	case cause != nil, strings.Contains(s, "error:"):
		category = "youtube_extractor_failure"
	}
	if category == "" {
		return nil
	}
	// Do not retain exec errors: custom runners may include arbitrary diagnostics.
	var safeCause error
	if errors.Is(cause, context.Canceled) {
		safeCause = context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		safeCause = context.DeadlineExceeded
	}
	var code *int
	var exit *exec.ExitError
	if errors.As(cause, &exit) {
		value := exit.ExitCode()
		code = &value
	}
	return &ExtractionError{Category: category, Cause: safeCause, ExitCode: code}
}
