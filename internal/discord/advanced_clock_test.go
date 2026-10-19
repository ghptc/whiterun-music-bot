package discord

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

func TestAdvancedDiscordAckSchedules(t *testing.T) {
	for _, fault := range []string{"429 within budget", "429 beyond budget", "deadline", "disconnected", "503"} {
		t.Run(fault, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, recorder := testBot(t)
				calls := 0
				firstPayload := ""
				client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithRateLimiter(&advancedBudgetLimiter{}), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if len(b.guilds) != 0 {
						t.Fatal("work before acknowledgement")
					}
					raw, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					if calls == 1 {
						firstPayload = string(raw)
					} else if string(raw) != firstPayload {
						t.Fatal("retry changed acknowledgement")
					}
					switch fault {
					case "deadline":
						<-req.Context().Done()
						return nil, req.Context().Err()
					case "disconnected":
						return nil, io.ErrUnexpectedEOF
					case "503":
						return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":"unavailable"}`))}, nil
					case "429 within budget", "429 beyond budget":
						if calls == 1 {
							after := "2"
							if fault == "429 beyond budget" {
								after = "3"
							}
							h := http.Header{}
							h.Set("Via", "discord")
							h.Set("Retry-After", after)
							h.Set("X-RateLimit-Bucket", "qa008")
							return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"message":"limited","retry_after":%s,"global":false}`, after)))}, nil
						}
					}
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				})}))
				defer client.Close(context.Background())
				sender := rest.New(client)
				e := interaction(t, "queue", true)
				var ackErr error
				e.Respond = func(typ d.InteractionResponseType, data d.InteractionResponseData, opts ...rest.RequestOpt) error {
					ackErr = sender.CreateInteractionResponse(e.ID(), e.Token(), d.InteractionResponse{Type: typ, Data: data}, opts...)
					return ackErr
				}
				start := time.Now()
				b.command(e)
				if time.Since(start) > 2700*time.Millisecond {
					t.Fatal("ack exceeded virtual interaction budget")
				}
				if fault == "429 within budget" {
					if ackErr != nil || calls != 2 || recorder.response == "" || len(b.guilds) != 1 {
						t.Fatalf("retry success not delivered: calls=%d err=%v", calls, ackErr)
					}
				} else {
					if ackErr == nil || len(b.guilds) != 0 || recorder.response != "" {
						t.Fatalf("failed ack caused work: calls=%d err=%v", calls, ackErr)
					}
				}
			})
		})
	}
}

// The stock disgo limiter has an unjoinable maintenance goroutine (QA008-D03).
// This injected limiter models retry timing only. Real limiter contract tests
// remain in queue_delivery_test.go; do not claim this proves their scheduling.
type advancedBudgetLimiter struct{ reset time.Time }

func (*advancedBudgetLimiter) MaxRetries() int       { return 3 }
func (*advancedBudgetLimiter) Close(context.Context) {}
func (l *advancedBudgetLimiter) Reset()              { l.reset = time.Time{} }
func (l *advancedBudgetLimiter) Wait(ctx context.Context, _ *rest.CompiledEndpoint) error {
	if deadline, ok := ctx.Deadline(); ok && !l.reset.IsZero() && l.reset.After(deadline) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(max(time.Duration(0), time.Until(l.reset)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (l *advancedBudgetLimiter) Unlock(_ *rest.CompiledEndpoint, response *http.Response) error {
	if response != nil && response.StatusCode == 429 {
		seconds, err := strconv.ParseFloat(response.Header.Get("Retry-After"), 64)
		if err != nil {
			return err
		}
		l.reset = time.Now().Add(time.Duration(seconds * float64(time.Second)))
	}
	return nil
}
