package player

import (
	"context"
	"errors"
	"testing"
	"time"
	"whiterun/internal/media"
)

func TestForcedFailureReportsOriginalAndAdvances(t *testing.T) {
	started := make(chan string, 4)
	failure := make(chan PlaybackEvent, 1)
	p, _ := newTestPlayer(t, func(ctx context.Context, tr media.Track, v Voice) error {
		started <- tr.VideoID
		if tr.ForcedYouTube {
			return errors.New("requested video unavailable")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	p.SetOnSkip(func(e PlaybackEvent) { failure <- e })
	exact := media.Track{ForcedYouTube: true, RequestedSource: "youtube", VideoID: "abcdefghijk", URL: "https://www.youtube.com/watch?v=abcdefghijk", Title: "Requested interview"}
	if err := p.EnqueueMany([]media.Track{exact, {VideoID: "mnopqrstuvw", Title: "next"}}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "abcdefghijk")
	receive(t, started, "mnopqrstuvw")
	select {
	case e := <-failure:
		if e.Track.VideoID != exact.VideoID || !e.Track.ForcedYouTube || e.SkippedReason == "" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("failure hidden")
	}
	select {
	case id := <-started:
		t.Fatal("extra replacement", id)
	default:
	}
}
