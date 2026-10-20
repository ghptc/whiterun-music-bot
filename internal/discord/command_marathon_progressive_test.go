package discord

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
)

func marathonPlaylist(count int) []media.Track {
	tracks := make([]media.Track, count)
	for i := range tracks {
		tracks[i] = marathonTrack(i+1, "p", count)
	}
	return tracks
}
func marathonResolution(firstBatch, later <-chan struct{}) func(context.Context, string) (media.Track, error) {
	return func(ctx context.Context, q string) (media.Track, error) {
		if strings.HasPrefix(strings.TrimSpace(q), "p") {
			n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(q), "p"))
			if err != nil {
				return media.Track{}, err
			}
			var gate <-chan struct{}
			if n > 7 {
				gate = later
			} else if n > 1 {
				gate = firstBatch
			}
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return media.Track{}, ctx.Err()
				}
			}
		}
		return media.Track{VideoID: "abcdefghijk", URL: "https://www.youtube.com/watch?v=abcdefghijk"}, nil
	}
}
func marathonMoveHead(m *marathonSession, w marathonWant, title string, user snowflake.ID) marathonWant {
	m.t.Helper()
	index := slices.Index(w.queue, title)
	if index < 0 {
		m.t.Fatal("missing original slot", title)
	}
	w.queue = append([]string{title}, append(append([]string{}, w.queue[:index]...), w.queue[index+1:]...)...)
	return m.command("move", []map[string]any{marathonOption("from", 4, index+1), marathonOption("to", 4, 1)}, user, w)
}
func marathonB(t *testing.T) *marathonSession {
	m := newMarathon(t)
	batch, later := make(chan struct{}), make(chan struct{})
	m.b.resolver.Search = marathonResolution(batch, later)
	tracks := marathonPlaylist(302)
	m.metadata(tracks)
	w := marathonWant{current: "p001", queue: marathonNames(tracks[1:]), history: []string{"p001"}}
	m.command("play", marathonPlayOptions(1, true), 3, w)
	if len(m.g.player.State().Resolving) != 1 {
		t.Fatal("playlist is not resolving")
	}
	w = m.command("shuffle", nil, 3, w)
	// The initial prefetch may include position 1 before playback dequeues it.
	// Expose position 7 first so both legal initial windows reach six ready slots.
	w = marathonMoveHead(m, w, "p007", 3)
	close(batch)
	synctest.Wait()
	w = marathonMoveHead(m, w, "p302", 3)
	m.command("queue", nil, 3, w)
	m.check(w, "six ready before EOF")
	ready, pending := 0, 0
	for _, e := range m.g.player.State().Entries {
		if e.Pending {
			pending++
		} else {
			ready++
		}
	}
	if ready != 6 || pending != 295 {
		t.Fatalf("production signature: ready=%d pending=%d", ready, pending)
	}
	w.current = ""
	m.eof(w)
	if !m.logs.has("scheduler_notification", "wakeup_reason", "playback_end") || !m.logs.has("scheduler_wait", "next_expected_position", 302) {
		t.Fatal("FIX-014 EOF notification or expected pending head missing")
	}
	close(later)
	synctest.Wait()
	w.current = "p302"
	w.queue = w.queue[1:]
	w.history = append(w.history, "p302")
	m.check(w, "resolution completed after EOF")
	if !m.logs.has("resolution_position_commit", "position", 302) || !m.logs.has("scheduler_wakeup", "wakeup_reason", "resolution_commit") || !m.logs.has("next_track_selected", "position", 302) {
		t.Fatal("missing automatic resolution/selection causal chain")
	}
	if m.logs.has("command_received", "name", "skip") {
		t.Fatal("journey relied on manual skip")
	}
	m.command("queue", nil, 3, w)
	w.current = ""
	w.queue = nil
	w.loop = "off"
	m.command("stop", nil, 3, w)
	m.finishJourney(w)
	return m
}
func marathonE(t *testing.T) *marathonSession {
	m := newMarathon(t)
	release := make(chan struct{})
	m.b.resolver.Search = marathonResolution(release, release)
	tracks := marathonPlaylist(10)
	m.metadata(tracks)
	w := marathonWant{current: "p001", queue: marathonNames(tracks[1:]), history: []string{"p001"}}
	m.command("play", marathonPlayOptions(1, true), 3, w)
	m.b.client.Caches.AddVoiceState(d.VoiceState{GuildID: 1, UserID: 5, ChannelID: new(snowflake.ID(6))})
	before := m.g.player.State()
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var acks atomic.Int32
	// Queue from another permitted listener races with a forbidden stop from
	// another channel. Neither operation may change the ongoing session.
	for _, request := range []struct {
		name string
		user snowflake.ID
	}{{"queue", 4}, {"stop", 5}} {
		e := m.interactionFor(request.name, 1, request.user)
		e.Respond = func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error {
			acks.Add(1)
			return nil
		}
		wg.Add(1)
		go func() { defer wg.Done(); <-gate; m.b.command(e) }()
	}
	close(gate)
	wg.Wait()
	synctest.Wait()
	if acks.Load() != 2 || !reflect.DeepEqual(before, m.g.player.State()) {
		t.Fatal("concurrent query/permission rejection changed playback")
	}
	m.seen["queue"], m.seen["stop"] = true, true
	m.check(w, "concurrent listeners")
	for _, id := range []string{fmt.Sprint(100 + m.serial - 1), fmt.Sprint(100 + m.serial)} {
		if !m.logs.has("command_total", "interaction_id", id) {
			t.Fatal("concurrent command never completed", id)
		}
	}
	w.paused = true
	m.command("pause", nil, 4, w)
	w.paused = false
	m.command("resume", nil, 3, w)
	m.metadata([]media.Track{marathonTrack(99, "song", 1)})
	w.queue = append(w.queue, "song099")
	m.command("play", marathonPlayOptions(99, false), 4, w)
	for _, entry := range m.g.player.State().Entries {
		if entry.Track.Title == "song099" && entry.Track.RequestedBy != "4" {
			t.Fatal("concurrent session lost requester ownership")
		}
	}
	w = marathonMoveHead(m, w, "p010", 4)
	w.current = ""
	m.command("skip", nil, 3, w)
	close(release)
	synctest.Wait()
	w.current = "p010"
	w.queue = w.queue[1:]
	w.history = append(w.history, "p010")
	m.check(w, "pending resolution after interleaved controls")
	m.command("queue", nil, 4, w)
	w.current = ""
	w.queue = nil
	w.loop = "off"
	m.command("stop", nil, 4, w)
	m.finishJourney(w)
	return m
}
