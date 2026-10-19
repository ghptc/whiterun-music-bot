package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type queueFailRest struct {
	rest.Rest
	calls []d.MessageUpdate
	fail  int
}

func (r *queueFailRest) UpdateInteractionResponse(_ snowflake.ID, _ string, m d.MessageUpdate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.calls = append(r.calls, m)
	if len(r.calls) <= r.fail {
		return nil, fmt.Errorf("controlled delivery failure")
	}
	return &d.Message{}, nil
}
func TestQueuePartialDeliveryFallback(t *testing.T) {
	for _, fail := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			b, r := testBot(t)
			sender := &queueFailRest{Rest: r, fail: fail}
			b.client.Rest = sender
			b.replyQueue(interaction(t, "queue", true), queueFixture(500), nil)
			want := 2
			if fail > 1 {
				want = 3
			}
			if len(sender.calls) != want {
				t.Fatal(len(sender.calls))
			}
			first := sender.calls[0]
			if len(first.Files) != 2 || first.Embeds == nil || len(*first.Embeds) != 10 {
				t.Fatal("missing rich attempt")
			}
			second := sender.calls[1]
			if len(second.Files) != 1 || len(*second.Embeds) != 0 || !strings.Contains(*second.Content, "preview unavailable") {
				t.Fatal("missing text fallback")
			}
			text := queueAttachment(t, second, "queue.txt")
			if !strings.Contains(text, "Song 0500") {
				t.Fatal("fallback lost queue tail")
			}
			if fail > 1 && (!strings.Contains(*sender.calls[2].Content, "could not be delivered completely") || len(sender.calls[2].Files) != 0) {
				t.Fatal("silent incomplete response")
			}
		})
	}
}

type queueTransport func(*http.Request) (*http.Response, error)

func (f queueTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestQueueRateLimitRetriesWholeMultipart(t *testing.T) {
	// Exercise the real disgo REST encoder + limiter through deterministic HTTP.
	b, _ := testBot(t)
	calls := 0
	var firstAt time.Time
	client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if _, ok := req.Context().Deadline(); !ok {
			t.Fatal("missing bounded delivery context")
		}
		_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(req.Body, params["boundary"])
		files := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			files[part.FileName()] = string(data)
		}
		if !strings.Contains(files["queue.html"], "Song 0500") || !strings.Contains(files["queue.txt"], "Song 0500") {
			t.Fatal("multipart retry lost complete attachments")
		}
		if calls == 1 {
			firstAt = time.Now()
			h := http.Header{}
			h.Set("X-RateLimit-Bucket", "queue-test")
			h.Set("Via", "discord")
			h.Set("Retry-After", "1")
			return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"rate limited","retry_after":1,"global":false}`))}, nil
		}
		if time.Since(firstAt) < time.Second {
			t.Fatal("ignored Retry-After")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"7","channel_id":"5"}`))}, nil
	})}))
	defer client.Close(context.Background())
	b.client.Rest = rest.New(client)
	b.replyQueue(interaction(t, "queue", true), queueFixture(500), nil)
	if calls != 2 {
		t.Fatal("unexpected request count", calls)
	}
}

func TestQueueLongRateLimitDoesNotBurstOrWaitForExpiry(t *testing.T) {
	b, _ := testBot(t)
	calls := 0
	client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		h := http.Header{}
		h.Set("X-RateLimit-Bucket", "long-queue-test")
		h.Set("Via", "discord")
		h.Set("Retry-After", "60")
		return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"rate limited","retry_after":60}`))}, nil
	})}))
	defer client.Close(context.Background())
	b.client.Rest = rest.New(client)
	start := time.Now()
	b.replyQueue(interaction(t, "queue", true), queueFixture(123), nil)
	if calls != 1 || time.Since(start) > time.Second {
		t.Fatal("burst or blocked beyond request budget", calls, time.Since(start))
	}
}

func TestQueueAdvertisedAttachmentBudget(t *testing.T) {
	b, r := testBot(t)
	e := interaction(t, "queue", true)
	// The limit is part of the actual Discord interaction payload.
	raw := `{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","attachment_size_limit":100,"user":{"id":"3"},"data":{"id":"4","name":"queue","type":1}}`
	if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	b.replyQueue(e, queueFixture(500), nil)
	if len(r.update.Files) != 0 || !strings.Contains(r.response, "No complete list was delivered") {
		t.Fatal("attachment cap ignored", r.response)
	}
}

type queueBlockingRest struct {
	rest.Rest
	entered, release chan struct{}
}

func (r *queueBlockingRest) UpdateInteractionResponse(_ snowflake.ID, _ string, _ d.MessageUpdate, _ ...rest.RequestOpt) (*d.Message, error) {
	close(r.entered)
	<-r.release
	return &d.Message{}, nil
}
func TestQueueSlowDeliveryDoesNotHoldPlayerLock(t *testing.T) {
	b, r := testBot(t)
	p := b.getGuild(1).player
	sender := &queueBlockingRest{Rest: r, entered: make(chan struct{}), release: make(chan struct{})}
	b.client.Rest = sender
	done := make(chan struct{})
	go func() { defer close(done); b.replyQueue(interaction(t, "queue", true), p.State(), nil) }()
	<-sender.entered
	responsive := make(chan struct{})
	go func() { p.Clear(); _ = p.State(); close(responsive) }()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		close(sender.release)
		<-done
		t.Fatal("REST send held player lock")
	}
	close(sender.release)
	<-done
}
