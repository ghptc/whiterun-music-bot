package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type queueTransport func(*http.Request) (*http.Response, error)

func (f queueTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type queueFailRest struct {
	rest.Rest
	updates   []d.MessageUpdate
	followups []d.MessageCreate
	failAt    int
}

func (r *queueFailRest) UpdateInteractionResponse(_ snowflake.ID, _ string, m d.MessageUpdate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.updates = append(r.updates, m)
	return &d.Message{}, nil
}
func (r *queueFailRest) CreateFollowupMessage(_ snowflake.ID, _ string, m d.MessageCreate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.followups = append(r.followups, m)
	if len(r.followups) == r.failAt {
		return nil, fmt.Errorf("controlled send failure")
	}
	return &d.Message{}, nil
}
func TestQueuePartialDeliveryPreservesFirstCardAndReportsProgress(t *testing.T) {
	for _, failAt := range []int{1, 2, 10} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			b, r := testBot(t)
			sender := &queueFailRest{Rest: r, failAt: failAt}
			b.client.Rest = sender
			b.replyQueue(interaction(t, "queue", true), queueFixture(500), nil)
			if len(sender.followups) != failAt || len(sender.updates) != 2 {
				t.Fatal("unexpected retry or continuation", len(sender.followups), len(sender.updates))
			}
			failure := sender.updates[1]
			text := updateText(failure)
			if !strings.Contains(text, fmt.Sprintf("Delivery incomplete · %d/500", failAt*12)) || !strings.Contains(text, "Song 0001") || len(failure.Files) != 0 || failure.Embeds != nil {
				t.Fatal(text)
			}
		})
	}
}
func TestQueueCompleteDeliveryUsesOneContainerPerBlock(t *testing.T) {
	for _, n := range []int{1, 10, 20, 123, 500, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b, r := testBot(t)
			b.replyQueue(interaction(t, "queue", true), queueFixture(n), nil)
			want := (n + 11) / 12
			if len(r.followups) != want-1 {
				t.Fatal(len(r.followups), want)
			}
			if len(r.update.Files) != 0 {
				t.Fatal("unexpected attachment")
			}

			text := r.queueText()
			for i := 1; i <= n; i++ {
				if strings.Count(text, fmt.Sprintf("%d · **Song %04d**", i, i)) != 1 {
					t.Fatal("omitted/duplicated position", i)
				}
			}

			for _, message := range r.followups {
				if len(message.Components) != 1 || len(message.Embeds) > 0 || len(message.Files) != 0 || message.Content != "" || message.Flags&d.MessageFlagIsComponentsV2 == 0 || message.Flags&d.MessageFlagEphemeral != 0 || message.AllowedMentions == nil {
					t.Fatal(message)
				}
				assertV2Budget(t, message.Components[0].(d.ContainerComponent))
			}
		})
	}
}
func TestQueueRateLimitRetriesSameJSONBlock(t *testing.T) {
	b, _ := testBot(t)
	calls := 0
	var firstAt time.Time
	firstPayload := ""
	client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if _, ok := req.Context().Deadline(); !ok {
			t.Fatal("unbounded REST call")
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["content"]; ok {
			t.Fatal("legacy content in V2 payload")
		}
		if _, ok := payload["embeds"]; ok {
			t.Fatal("legacy embeds in V2 payload")
		}
		if payload["flags"] != float64(d.MessageFlagIsComponentsV2) || len(payload["components"].([]any)) != 1 || strings.Contains(string(raw), "queue.html") || strings.Contains(string(raw), "queue.txt") {
			t.Fatal(string(raw))
		}
		if calls == 1 {
			firstAt = time.Now()
			firstPayload = string(raw)
			h := http.Header{}
			h.Set("X-RateLimit-Bucket", "queue-v2")
			h.Set("Via", "discord")
			h.Set("Retry-After", "1")
			return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"rate limited","retry_after":1,"global":false}`))}, nil
		}
		if calls == 2 && (time.Since(firstAt) < time.Second || string(raw) != firstPayload) {
			t.Fatal("429 did not retry identical block after delay")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"7","channel_id":"5"}`))}, nil
	})}))
	defer client.Close(context.Background())
	b.client.Rest = rest.New(client)
	b.replyQueue(interaction(t, "queue", true), queueFixture(13), nil)
	// Original edit twice (429), one followup, one completion edit.
	if calls != 4 {
		t.Fatal("unexpected requests", calls)
	}
}
func TestQueueLongRateLimitStopsWithoutBurst(t *testing.T) {
	b, _ := testBot(t)
	calls := 0
	client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		h := http.Header{}
		h.Set("X-RateLimit-Bucket", "long-v2")
		h.Set("Via", "discord")
		h.Set("Retry-After", "60")
		return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"rate limited","retry_after":60}`))}, nil
	})}))
	defer client.Close(context.Background())
	b.client.Rest = rest.New(client)
	started := time.Now()
	b.replyQueue(interaction(t, "queue", true), queueFixture(123), nil)
	if calls != 1 || time.Since(started) > time.Second {
		t.Fatal("burst or waited beyond request budget", calls, time.Since(started))
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
	e := interaction(t, "queue", true)
	state := p.State()
	go func() { defer close(done); b.replyQueue(e, state, nil) }()
	<-sender.entered
	responsive := make(chan struct{})
	go func() { p.Clear(); _ = p.State(); close(responsive) }()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		close(sender.release)
		<-done
		t.Fatal("REST held player lock")
	}
	close(sender.release)
	<-done
}
func TestQueueCancellationIsScopedToGuildAndChannel(t *testing.T) {
	b, _ := testBot(t)
	one, two := b.getGuild(1), b.getGuild(2)
	old, done := one.beginQueueDelivery(b.ctx, 5)
	defer done()
	otherChannel, finishChannel := one.beginQueueDelivery(b.ctx, 6)
	defer finishChannel()
	otherGuild, finishGuild := two.beginQueueDelivery(b.ctx, 5)
	defer finishGuild()
	newer, finishNew := one.beginQueueDelivery(b.ctx, 5)
	defer finishNew()
	if old.Err() != context.Canceled || newer.Err() != nil || otherChannel.Err() != nil || otherGuild.Err() != nil {
		t.Fatal("bad cancellation scope")
	}
	done() // Old cleanup must not cancel or delete the newer delivery.
	third, finishThird := one.beginQueueDelivery(b.ctx, 5)
	defer finishThird()
	if newer.Err() != context.Canceled || third.Err() != nil {
		t.Fatal("stale cleanup deleted newer delivery")
	}
}
func TestQueuePersonalInstallRefusesOversizedDeliveryExplicitly(t *testing.T) {
	b, r := testBot(t)
	e := interaction(t, "queue", true)
	raw := `{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","authorizing_integration_owners":{"1":"3"},"user":{"id":"3"},"data":{"id":"4","name":"queue","type":1}}`
	if err := json.Unmarshal([]byte(raw), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	b.replyQueue(e, queueFixture(123), nil)
	if len(r.followups) != 0 || !strings.Contains(r.response, "Install the Bard in this server") {
		t.Fatal(r.response)
	}
}
func BenchmarkQueueV2(b *testing.B) {
	for _, n := range []int{1, 10, 20, 123, 500, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			state := queueFixture(n)
			b.ReportAllocs()
			for b.Loop() {
				view, err := buildQueueView(state, nil)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(view.Components), "components")
				b.ReportMetric(float64(len(view.Blocks)), "messages")
				b.ReportMetric(float64(view.Missing), "missing-artworks")
			}
		})
	}
}

type queueUpdateFailureRest struct {
	rest.Rest
	updates, followups int
}

func (r *queueUpdateFailureRest) UpdateInteractionResponse(_ snowflake.ID, _ string, _ d.MessageUpdate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.updates++
	return nil, fmt.Errorf("controlled update failure")
}
func (r *queueUpdateFailureRest) CreateFollowupMessage(_ snowflake.ID, _ string, _ d.MessageCreate, _ ...rest.RequestOpt) (*d.Message, error) {
	r.followups++
	return &d.Message{}, nil
}
func TestQueueInitialSendAndNoticeFailureStopDelivery(t *testing.T) {
	b, recorder := testBot(t)
	sender := &queueUpdateFailureRest{Rest: recorder}
	b.client.Rest = sender
	b.replyQueue(interaction(t, "queue", true), queueFixture(500), nil)
	if sender.updates != 2 || sender.followups != 0 {
		t.Fatal("continued after failed initial send", sender.updates, sender.followups)
	}
	g := b.getGuild(1)
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	if len(g.queueDeliveries) != 0 {
		t.Fatal("failed delivery leaked")
	}
}
