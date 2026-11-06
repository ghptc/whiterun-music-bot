// Package spotifyconnect controls an independently managed local go-librespot.
// It deliberately has no Discord audio path. API success acknowledges a command,
// not audible playback; consumers must validate subsequent track identity.
package spotifyconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrURI         = errors.New("spotify_connect_invalid_uri")
	ErrAuth        = errors.New("spotify_connect_no_session")
	ErrRejected    = errors.New("spotify_connect_command_rejected")
	ErrUnavailable = errors.New("spotify_connect_unavailable")
	ErrRateLimited = errors.New("spotify_connect_rate_limited")
	ErrNetwork     = errors.New("spotify_connect_connection_lost")
	ErrProtocol    = errors.New("spotify_connect_invalid_response")
	ErrIdentity    = errors.New("spotify_connect_identity_mismatch")
)
var trackURI = regexp.MustCompile(`^spotify:track:[A-Za-z0-9]{22}$`)

// Client is safe for concurrent requests. It never retries commands: a timeout
// may mean the daemon accepted a command. The session owner must reconcile it.
// Construction accepts only numeric loopback HTTP endpoints, without credentials.
type Client struct {
	base string
	http *http.Client
}

func New(endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, ErrRejected
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, ErrRejected
	}
	return &Client{base: "http://" + u.Host, http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if json.NewEncoder(&buf).Encode(body) != nil {
			return ErrProtocol
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &buf)
	if err != nil {
		return ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrNetwork
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 204:
		return ErrAuth // upstream uses 204 for ErrNoSession, including controls
	case 401:
		return ErrAuth
	case 403, 409, 400:
		return ErrRejected
	case 404:
		return ErrUnavailable
	case 429:
		return ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	if out != nil {
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out) != nil {
			return ErrProtocol
		}
	}
	return nil
}
func (c *Client) Play(ctx context.Context, uri string) error { return c.PlayAt(ctx, uri, 0, false) }
func (c *Client) PlayAt(ctx context.Context, uri string, position int64, paused bool) error {
	if !trackURI.MatchString(uri) || position < 0 {
		return ErrURI
	}
	return c.request(ctx, "POST", "/player/play", struct {
		URI      string `json:"uri"`
		Position int64  `json:"position"`
		Paused   bool   `json:"paused"`
	}{uri, position, paused}, nil)
}

// SingleTrack disables daemon queue behavior; Bard remains the queue owner.
func (c *Client) SingleTrack(ctx context.Context) error {
	for _, setting := range []string{"repeat_track", "repeat_context", "shuffle_context"} {
		if err := c.request(ctx, "POST", "/player/"+setting, map[string]bool{setting: false}, nil); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) Pause(ctx context.Context) error {
	return c.request(ctx, "POST", "/player/pause", nil, nil)
}
func (c *Client) Resume(ctx context.Context) error {
	return c.request(ctx, "POST", "/player/resume", nil, nil)
}
func (c *Client) Stop(ctx context.Context) error {
	return c.request(ctx, "POST", "/player/stop", nil, nil)
}

type Track struct {
	URI      string `json:"uri"`
	Position int64  `json:"position"`
	Duration int64  `json:"duration"`
}
type PlaybackStatus struct {
	Paused  bool   `json:"paused"`
	Stopped bool   `json:"stopped"`
	Track   *Track `json:"track"`
}

func (c *Client) Status(ctx context.Context) (PlaybackStatus, error) {
	var s PlaybackStatus
	err := c.request(ctx, "GET", "/status", nil, &s)
	return s, err
}
func (s PlaybackStatus) ValidateIdentity(expected string) error {
	if !trackURI.MatchString(expected) {
		return ErrURI
	}
	if s.Track == nil || s.Track.URI != expected {
		return ErrIdentity
	}
	return nil
}

// Event excludes upstream human-readable error messages to avoid propagating
// secrets. Inactive means interrupted ownership, not proven authentication expiry.
type Event struct {
	Type string `json:"type"`
	Data struct {
		URI        string `json:"uri"`
		PlayOrigin string `json:"play_origin"`
		Stage      string `json:"stage"`
		Kind       string `json:"kind"`
		Unplayable bool   `json:"unplayable"`
		Value      bool   `json:"value"`
	} `json:"data"`
}

// Events blocks until disconnect/cancellation or the synchronous callback fails.
// It reconnects neither the socket nor playback. Call before issuing Play.
func (c *Client) Events(ctx context.Context, handle func(Event) error) error {
	return c.EventsReady(ctx, nil, handle)
}
func (c *Client) EventsReady(ctx context.Context, ready chan<- struct{}, handle func(Event) error) error {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Proxy: nil}
	conn, resp, err := dialer.DialContext(ctx, "ws"+c.base[4:]+"/events", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrNetwork
	}
	defer conn.Close()
	conn.SetReadLimit(64 << 10)
	if ready != nil {
		close(ready)
	}
	done := make(chan struct{})
	exited := make(chan struct{})

	conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(40 * time.Second)) })
	go func() {
		defer close(exited)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	defer func() { close(done); <-exited }()
	for {
		var event Event
		if conn.ReadJSON(&event) != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrNetwork
		}
		if err := handle(event); err != nil {
			return err
		}
	}
}
