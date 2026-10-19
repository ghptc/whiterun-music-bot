package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

// Exercise the real disgo HTTP and rate limiter path, not only a Respond stub.
func TestQueueAcknowledgementHTTPBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		delay         time.Duration
		fail, blocked bool
		rateLimited   bool
	}{
		{name: "immediate"}, {name: "above_old_two_second_cutoff", delay: 2100 * time.Millisecond},
		{name: "rate_limit_exceeds_budget", rateLimited: true},
		{name: "network_failure", fail: true}, {name: "blocked", blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, recorder := testBot(t)
			var ackErr error
			calls := 0
			client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > 2700*time.Millisecond {
					t.Error("ack deadline missing or too long")
				}
				if len(b.guilds) != 0 {
					t.Error("guild work before ack")
				}
				if tc.blocked {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				if tc.rateLimited {
					h := http.Header{"Via": []string{"discord"}, "Retry-After": []string{"60"}, "X-Ratelimit-Bucket": []string{"ack"}}
					return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"rate limited","retry_after":60}`))}, nil
				}
				if tc.fail {
					return nil, fmt.Errorf("controlled network failure")
				}
				select {
				case <-time.After(tc.delay):
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}))
			defer client.Close(context.Background())
			sender := rest.New(client)
			e := interaction(t, "queue", true)
			e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, opts ...rest.RequestOpt) error {
				ackErr = sender.CreateInteractionResponse(e.ID(), e.Token(), d.InteractionResponse{Type: typ, Data: data}, opts...)
				return ackErr
			}
			started := time.Now()
			b.command(e)
			if calls != 1 {
				t.Fatalf("unexpected ack retries: %d", calls)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("ack exceeded interaction window")
			}
			if tc.fail || tc.blocked || tc.rateLimited {
				if ackErr == nil || recorder.response != "" || len(b.guilds) != 0 {
					t.Fatal("failed ack proceeded to queue work")
				}
			} else if ackErr != nil || recorder.response == "" {
				t.Fatalf("ack=%v response=%s", ackErr, recorder.response)
			}
		})
	}
}

// Three blocked Spotify workers and a running voice callback must not delay
// acknowledgements, detached snapshots, skip or another guild's queue.
func TestQueueDuringSpotifyImportAndSkip(t *testing.T) {
	b, recorder := testBot(t)
	g := b.getGuild(1)
	g.player.Close(context.Background())
	playing := make(chan struct{}, 1)
	g.player = player.New(b.ctx, b.log, func(ctx context.Context, _ media.Track, _ player.Voice) error {
		select {
		case playing <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err := g.player.Attach(transport{conn: &dummyConn{}}); err != nil {
		t.Fatal(err)
	}
	tracks := make([]media.Track, 123)
	for i := range tracks {
		tracks[i] = media.Track{Title: fmt.Sprint(i)}
	}
	workers := make(chan struct{}, 123)
	job, err := g.player.BeginCollection(b.ctx, media.Collection{Tracks: tracks, IsCollection: true}, func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if first {
			track.URL = "https://youtube.com/watch?v=abcdefghijk"
			return track, nil
		}
		workers <- struct{}{}
		<-ctx.Done()
		return media.Track{}, ctx.Err()
	}, g.player.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-job.First:
		if first.Err != nil {
			t.Fatal(first.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("first track blocked")
	}
	for range 3 {
		select {
		case <-workers:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-playing:
	case <-time.After(time.Second):
		t.Fatal("audio did not start")
	}
	acked := make(chan struct{})
	e := interaction(t, "queue", true)
	e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error {
		close(acked)
		return nil
	}
	done := make(chan struct{})
	go func() { defer close(done); b.command(e) }()
	select {
	case <-acked:
	case <-time.After(time.Second):
		t.Fatal("ack blocked behind music work")
	}
	if _, err := g.player.SkipN(1); err != nil {
		t.Fatal(err)
	}
	_ = g.player.State()
	otherDone := make(chan struct{})
	otherEvent := interaction(t, "queue", true)
	if err := json.Unmarshal([]byte(`{"id":"11","application_id":"2","type":2,"token":"other","guild_id":"2","user":{"id":"3"},"data":{"id":"4","name":"queue","type":1}}`), &otherEvent.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(otherDone); b.command(otherEvent) }()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("other guild starved")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queue delivery did not finish")
	}
	if recorder.response == "" {
		t.Fatal("queue response missing")
	}
	g.player.Clear()
	select {
	case <-job.Done:
	case <-time.After(time.Second):
		t.Fatal("collection workers leaked")
	}
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	if len(g.queueDeliveries) != 0 {
		t.Fatal("delivery resources leaked")
	}
}
