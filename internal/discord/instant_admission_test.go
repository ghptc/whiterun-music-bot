package discord

import (
	"strings"
	"testing"
	"testing/synctest"
	"whiterun/internal/media"
)

func TestInstantSearchAdmissionWaitsWithoutAddingWorkers(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "permit-released", true: "search-canceled"}[cancel], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, r := testBot(t)
				g, _ := idleGuild(t, b, 1)
				listener(b, 1, 3, false)
				b.searches = make(chan struct{}, 4)
				for range 4 {
					b.searches <- struct{}{}
				}
				b.resolver.Spotify = hardeningSpotify{err: media.ErrSpotifyAuth}
				done := make(chan struct{})
				go func() {
					defer close(done)
					b.command(qualityInteraction(t, "play", 3, "https://open.spotify.com/track/0123456789012345678901"))
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("fifth command was rejected instead of waiting")
				default:
				}
				want := media.ErrSpotifyAuth.Error()
				held := 3
				if cancel {
					g.searchCancel()
					want = "Playback changed while searching"
					held = 4
				} else {
					<-b.searches
				}
				synctest.Wait()
				<-done
				if !strings.Contains(r.queueText(), want) || len(b.searches) != held {
					t.Fatal("lost cancellation reason or leaked admission slot", r.queueText(), len(b.searches))
				}
			})
		})
	}
}
