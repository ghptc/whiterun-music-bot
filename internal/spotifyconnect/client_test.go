package spotifyconnect

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const uri = "spotify:track:0123456789ABCDEFGHIJKL"

func TestControlsAndIdentity(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path == "/status" {
			w.Write([]byte(`{"track":{"uri":"` + uri + `","position":123}}`))
			return
		}
		if r.URL.Path == "/player/play" {
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["uri"] != uri {
				t.Error(payload)
			}
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	c, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	for _, bad := range []string{"", uri + "x", "spotify:album:0123456789ABCDEFGHIJKL", "https://open.spotify.com/track/0123456789ABCDEFGHIJKL"} {
		if !errors.Is(c.Play(ctx, bad), ErrURI) {
			t.Fatal(bad)
		}
	}
	if count.Load() != 0 {
		t.Fatal("invalid URI sent")
	}
	for _, op := range []func(context.Context) error{func(ctx context.Context) error { return c.Play(ctx, uri) }, c.Pause, c.Resume, c.Stop} {
		if err := op(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Status(ctx)
	if err != nil || s.ValidateIdentity(uri) != nil || s.Track.Position != 123 {
		t.Fatal(s, err)
	}
	if !errors.Is(s.ValidateIdentity("spotify:track:1123456789ABCDEFGHIJKL"), ErrIdentity) {
		t.Fatal("accepted relink")
	}
}
func TestFailuresSanitizedNoRetry(t *testing.T) {
	for _, status := range []int{204, 401, 403, 404, 409, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				w.Write([]byte("sensitive-response"))
			}))
			defer s.Close()
			c, _ := New(s.URL)
			defer c.Close()
			err := c.Play(context.Background(), uri)
			if err == nil || strings.Contains(err.Error(), "sensitive") || count.Load() != 1 {
				t.Fatal(err, count.Load())
			}
			if (status == 204 || status == 401) && !errors.Is(err, ErrAuth) {
				t.Fatal(err)
			}
		})
	}
}
func TestEndpointPolicy(t *testing.T) {
	for _, u := range []string{"https://127.0.0.1:3678", "http://example.com", "http://user:secret@127.0.0.1", "http://127.0.0.1/token", "http://127.0.0.1?token=secret"} {
		if _, err := New(u); err == nil {
			t.Fatal(u)
		}
	}
}
func TestEventsCancellationAndDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disconnect"}[disconnect], func(t *testing.T) {
			ready := make(chan struct{})
			finished := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				defer close(finished)
				conn.WriteJSON(map[string]any{"type": "inactive", "data": map[string]string{"uri": uri}})
				close(ready)
				if !disconnect {
					conn.ReadMessage()
				}
			}))
			defer s.Close()
			c, _ := New(s.URL)
			defer c.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- c.Events(ctx, func(e Event) error {
					if e.Type != "inactive" || e.Data.URI != uri {
						t.Error(e)
					}
					return nil
				})
			}()
			<-ready
			if !disconnect {
				cancel()
			}
			select {
			case err := <-result:
				if disconnect && !errors.Is(err, ErrNetwork) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("reader leaked")
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("socket leaked")
			}
		})
	}
}
func TestTimeoutAndConcurrentCommands(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer s.Close()
	c, _ := New(s.URL)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if !errors.Is(c.Play(ctx, uri), context.DeadlineExceeded) {
		t.Fatal("deadline lost")
	}
}
