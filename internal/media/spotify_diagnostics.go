package media

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"syscall"
)

func (s *Spotify) stage(ctx context.Context, stage string, fields ...any) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	log.InfoContext(ctx, "Spotify metadata diagnostic", append([]any{"stage", stage}, fields...)...)
}

// Classify errors without logging URLs, headers, tokens or response bodies.
func spotifyErrorCategory(err error) string {
	if err == nil {
		return "none"
	}
	var fetch *spotifyFetchFailure
	if errors.As(err, &fetch) {
		return fetch.reason
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var u *url.Error
	if errors.As(err, &u) {
		return spotifyErrorCategory(u.Err)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsNotFound {
			return "dns_no_address"
		}
		if dns.IsTimeout {
			return "dns_timeout"
		}
		return "dns_failure"
	}
	if errors.Is(err, syscall.ENETUNREACH) {
		return "network_unreachable"
	}
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return "timeout"
	}
	if errors.Is(err, ErrSpotifyIncomplete) {
		return "incomplete"
	}
	if errors.Is(err, ErrSpotifyPageParse) {
		return "invalid_metadata"
	}
	return "transport_or_fetch"
}

// This carries only a fixed, sanitized reason through the safe user-facing error.
// The original transport error may contain sensitive data and is not retained.
type spotifyFetchFailure struct{ reason string }

func (e *spotifyFetchFailure) Error() string { return ErrSpotifyPageFetch.Error() }
func (e *spotifyFetchFailure) Unwrap() error { return ErrSpotifyPageFetch }
func safeSpotifyFetchFailure(err error) error {
	return &spotifyFetchFailure{reason: spotifyErrorCategory(err)}
}
