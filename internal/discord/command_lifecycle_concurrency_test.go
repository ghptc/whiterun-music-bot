package discord

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

func TestQA013ConcurrentPlayRequestsAndEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "playing", nil)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for _, user := range []int{3, 4} {
			e := qualityInteraction(t, "play", user, "https://open.spotify.com/track/0123456789012345678901")
			wg.Add(1)
			go func() { defer wg.Done(); <-gate; f.b.command(e) }()
		}
		close(gate)
		wg.Wait()
		synctest.Wait()
		f.model.queue = append(f.model.queue, "request", "request")
		f.check("concurrent play accepted")
		owners := map[string]int{}
		for _, entry := range f.g.player.State().Entries {
			if entry.Track.Title == "request" {
				owners[entry.Track.RequestedBy]++
			}
		}
		if owners["3"] != 1 || owners["4"] != 1 {
			t.Fatal("requests dropped, duplicated or misattributed", owners)
		}
		for range 5 {
			f.eof()
		}
		f.request("stop")
	})
}
func TestQA013ConcurrentQueueAndMutations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "playing", nil)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i := 0; i < 48; i++ {
			name := []string{"queue", "move", "shuffle", "remove"}[i%4]
			e := f.interaction(name)
			e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
			wg.Add(1)
			go func() { defer wg.Done(); <-gate; f.b.command(e) }()
		}
		close(gate)
		wg.Wait()
		synctest.Wait()
		s := f.g.player.State()
		if s.Current == nil || s.Current.Title != "a" {
			t.Fatal("queue editing interrupted active playback")
		}
		ids := lifecycleTitles(s.Queued)
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"b"}) && !slices.Equal(ids, []string{"c"}) && !slices.Equal(ids, []string{"d"}) {
			t.Fatal("edits lost/duplicated logical positions", ids)
		}
		f.model.queue = lifecycleTitles(s.Queued)
		f.check("concurrent ordered edits")
		f.eof()
		f.request("stop")
	})
}
func TestQA013EOFDuringDiscordSkip(t *testing.T) {
	for scenario := 0; scenario < 64; scenario++ {
		t.Run(fmt.Sprint(scenario), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, "playing", nil)
				gate := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				e := f.interaction("skip")
				e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }
				go func() { defer wg.Done(); <-gate; f.b.command(e) }()
				go func() { defer wg.Done(); <-gate; f.finish <- nil }()
				close(gate)
				wg.Wait()
				synctest.Wait()
				// EOF and skip may coalesce on the ending attempt or affect two consecutive
				// attempts. Both histories are legal; resurrected/duplicated tracks are not.
				s := f.g.player.State()
				if s.Current == nil {
					t.Fatal("concurrent EOF/skip stalled")
				}
				switch s.Current.Title {
				case "b":
					f.model.current = "b"
					f.model.queue = []string{"c", "d"}
					f.model.history = []string{"a", "b"}
				case "c":
					f.model.current = "c"
					f.model.queue = []string{"d"}
					f.model.history = []string{"a", "b", "c"}
					if slices.Equal(lifecycleTitles(f.g.player.History()), []string{"a", "c"}) {
						f.model.history = []string{"a", "c"}
					} // skipped before b first frame
				default:
					t.Fatal("illegal concurrent outcome", s.Current.Title)
				}
				f.check("EOF/skip linearization")
				f.eof()
				f.request("stop")
			})
		})
	}
}
