package player

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"

	"whiterun/internal/media"
)

// This oracle stores recording IDs and implements list transformations, not
// queueEntry, generation tickets, locks, or production helper functions.
type advancedModel struct {
	queue, history             []string
	current, loop              string
	attached, paused, autoplay bool
	serial                     int
}

func (m *advancedModel) settle() {
	if m.attached && m.current == "" && len(m.queue) > 0 {
		m.current = m.queue[0]
		m.queue = m.queue[1:]
		m.paused = false
		m.history = append(m.history, m.current)
		if len(m.history) > 100 {
			m.history = m.history[len(m.history)-100:]
		}
	}
}
func advancedTrack(id string) media.Track {
	return media.Track{Title: id, SpotifyID: "spotify-" + id, VideoID: "abcdefghijk", Artists: []string{"artist", id}, RequestedBy: "listener", Explicit: new(true), Position: 1, Total: 1}
}
func advancedIDs(tracks []media.Track) []string {
	out := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		out = append(out, tr.Title)
	}
	return out
}
func advancedCompare(t *testing.T, p *GuildPlayer, m *advancedModel, step int) {
	t.Helper()
	s := p.State()
	id := ""
	if s.Current != nil {
		id = s.Current.Title
	}
	if id != m.current || !slices.Equal(advancedIDs(s.Queued), m.queue) || !slices.Equal(advancedIDs(p.History()), m.history) || s.Loop != m.loop || s.Paused != m.paused || s.Autoplay != m.autoplay {
		t.Fatalf("step %d got current=%q queue=%v history=%v loop=%q paused=%v autoplay=%v; model=%+v", step, id, advancedIDs(s.Queued), advancedIDs(p.History()), s.Loop, s.Paused, s.Autoplay, m)
	}
	tracks := append([]media.Track{}, s.Queued...)
	if s.Current != nil {
		tracks = append(tracks, *s.Current)
	}
	tracks = append(tracks, p.History()...)
	for _, tr := range tracks {
		expected := advancedTrack(tr.Title)
		tr.Timing = nil
		if !reflect.DeepEqual(tr, expected) {
			t.Fatalf("step %d identity changed: %+v want %+v", step, tr, expected)
		}
	}
	if (m.current != "") != s.CurrentStarted {
		t.Fatalf("step %d first frame state differs", step)
	}
}

// Bytes form a replayable command language shared with native fuzzing. Each
// pair is (opcode, argument); all operations are followed by quiescence.
func advancedRunSequence(t *testing.T, sequence []byte) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan error)
		p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, v Voice) error {
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			select {
			case err := <-finish:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		defer p.Close(context.Background())
		other := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		defer other.Close(context.Background())
		otherBefore := other.State()
		m := &advancedModel{}
		for step := 0; step+1 < len(sequence); step += 2 {
			op, arg := int(sequence[step]%18), int(sequence[step+1])
			before := p.State()
			var err error
			wantError := false
			position := func() int {
				switch arg % 4 {
				case 0:
					return 0
				case 1:
					return len(m.queue) + 1
				default:
					if len(m.queue) == 0 {
						return 1
					}
					return arg%len(m.queue) + 1
				}
			}
			switch op {
			case 0: // summon
				err = p.Attach(&fakeVoice{})
				wantError = m.attached
				if !wantError {
					m.attached = true
				}
			case 1: // enqueue atomic batch, including exact capacity and overflow
				count := arg % 6
				if arg == 254 {
					count = 1000
				}
				if arg == 255 {
					count = 1001
				}
				tracks := make([]media.Track, count)
				ids := make([]string, count)
				for i := range tracks {
					m.serial++
					ids[i] = fmt.Sprint(m.serial)
					tracks[i] = advancedTrack(ids[i])
				}
				err = p.EnqueueMany(tracks, p.Ticket())
				wantError = !m.attached || len(m.queue)+count > 1000
				if !wantError {
					m.queue = append(m.queue, ids...)
				}
			case 2:
				i := position()
				err = p.Remove(i)
				wantError = i < 1 || i > len(m.queue)
				if !wantError {
					m.queue = append(m.queue[:i-1], m.queue[i:]...)
				}
			case 3:
				from, to := position(), arg/4%max(1, len(m.queue))+1
				err = p.Move(from, to)
				wantError = from < 1 || from > len(m.queue) || to > len(m.queue)
				if !wantError {
					id := m.queue[from-1]
					next := append([]string{}, m.queue[:from-1]...)
					next = append(next, m.queue[from:]...)
					out := append([]string{}, next[:to-1]...)
					out = append(out, id)
					m.queue = append(out, next[to-1:]...)
				}
			case 4:
				count := arg % 5
				if arg == 255 {
					count = 1002
				}
				result, e := p.SkipN(count)
				err = e
				wantError = count < 1 || count > 1001
				if !wantError {
					expected := 0
					if m.current != "" {
						m.current = ""
						m.paused = false
						expected++
					}
					remove := min(count-expected, len(m.queue))
					m.queue = m.queue[remove:]
					expected += remove
					if result.Skipped != expected || result.Remaining != len(m.queue) {
						t.Fatalf("skip result=%+v expected=%d", result, expected)
					}
				}
			case 5:
				i := position()
				err = p.Jump(i)
				wantError = i < 1 || i > len(m.queue)
				if !wantError {
					m.current = ""
					m.paused = false
					m.queue = m.queue[i-1:]
				}
			case 6:
				err = p.Replay()
				wantError = m.current == "" || len(m.queue) >= 1000
				if !wantError {
					m.queue = append([]string{m.current}, m.queue...)
					m.current = ""
					m.paused = false
				}
			case 7:
				err = p.Previous()
				n := len(m.history)
				if m.current != "" {
					n--
				}
				needed := 1
				if m.current != "" {
					needed++
				}
				wantError = n <= 0 || !m.attached || len(m.queue)+needed > 1000
				if !wantError {
					prefix := []string{m.history[n-1]}
					if m.current != "" {
						prefix = append(prefix, m.current)
					}
					m.history = m.history[:n-1]
					m.queue = append(prefix, m.queue...)
					m.current = ""
					m.paused = false
				}
			case 8:
				mode := []string{"off", "track", "queue", "invalid"}[arg%4]
				err = p.SetLoop(mode)
				wantError = mode == "invalid"
				if !wantError {
					m.loop = mode
				}
			case 9:
				paused := arg%2 == 0
				_, err = p.SetPaused(paused)
				wantError = m.current == ""
				if !wantError {
					m.paused = paused
				}
			case 10:
				// EOF or a failed partial stream. Failure must not loop.
				if m.current != "" {
					var failure error
					if arg%2 != 0 {
						failure = fmt.Errorf("injected audio failure")
					}
					finish <- failure
					if failure == nil && len(m.queue) < 1000 {
						switch m.loop {
						case "track":
							m.queue = append([]string{m.current}, m.queue...)
						case "queue":
							m.queue = append(m.queue, m.current)
						}
					}
					m.current = ""
					m.paused = false
				}
			case 11:
				removed := p.Clear()
				if removed != len(m.queue) {
					t.Fatal("clear count", removed, len(m.queue))
				}
				m.queue = nil
				m.loop = "off"
			case 12:
				p.Stop()
				m.queue = nil
				m.current = ""
				m.paused = false
				m.loop = "off"
			case 13:
				p.Leave(context.Background())
				m.attached = false
				m.queue = nil
				m.current = ""
				m.paused = false
				m.loop = "off"
			case 14:
				m.autoplay = arg%2 == 0
				p.SetAutoplay(m.autoplay)
			case 15:
				err = p.Seek(fmt.Sprintf("%d:00", arg))
				wantError = true
			case 16:
				ticket := p.Ticket()
				p.Stop()
				m.queue = nil
				m.current = ""
				m.paused = false
				m.loop = "off"
				err = p.Enqueue(advancedTrack("stale"), ticket)
				wantError = true
			case 17:
				old := append([]string{}, m.queue...)
				err = p.Shuffle()
				wantError = len(old) == 0
				if !wantError {
					got := advancedIDs(p.State().Queued)
					a, b := slices.Clone(old), slices.Clone(got)
					slices.Sort(a)
					slices.Sort(b)
					if !slices.Equal(a, b) {
						t.Fatal("shuffle changed recording multiset")
					}
					m.queue = got // shuffle order is intentionally unspecified
				}
			}
			if (err != nil) != wantError {
				t.Fatalf("step=%d op=%d arg=%d err=%v wantError=%v", step/2, op, arg, err, wantError)
			}
			if wantError && op != 16 && !reflect.DeepEqual(before, p.State()) {
				t.Fatalf("rejected op %d changed state", op)
			}
			m.settle()
			synctest.Wait()
			advancedCompare(t, p, m, step/2)
			if !reflect.DeepEqual(otherBefore, other.State()) {
				t.Fatal("other guild mutated")
			}
		}
	})
}
func TestAdvancedModelSequences(t *testing.T) {
	if testing.Short() {
		t.Skip("2048 model sequences; run outside -short")
	}
	sequences := 2048
	if raw := os.Getenv("BARD_QA_MODEL_SEED"); raw != "" {
		var seed uint64
		if _, err := fmt.Sscan(raw, &seed); err != nil {
			t.Fatal(err)
		}
		advancedSeed(t, seed)
		return
	}
	for seed := uint64(0); seed < uint64(sequences); seed++ {
		if !t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { advancedSeed(t, seed) }) {
			return
		}
	}
}
func advancedSeed(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x5141303038))
	// Start active, then explore 128 valid/invalid choices without terminating
	// early on an empty queue, as the older destructive queue model did.
	sequence := []byte{0, 0, 1, 5}
	for range 128 {
		sequence = append(sequence, byte(rng.IntN(18)), byte(rng.IntN(256)))
	}
	defer func() {
		if t.Failed() {
			record := struct {
				Seed uint64
				Hex  string
			}{seed, hex.EncodeToString(sequence)}
			data, _ := json.MarshalIndent(record, "", "  ")
			dir := os.Getenv("BARD_QA_FAILURE_DIR")
			if dir == "" {
				dir = filepath.Join(os.TempDir(), "bard-qa008-failures")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Log("persist failure", err)
				return
			}
			path := filepath.Join(dir, fmt.Sprintf("model-%d.json", seed))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Log(err)
			} else {
				t.Log("sequence saved", path)
			}
			t.Logf("replay BARD_QA_MODEL_SEED=%d go test ./internal/player -run '^TestAdvancedModelSequences$' -count=1", seed)
		}
	}()
	advancedRunSequence(t, sequence)
}
func FuzzAdvancedQueue(f *testing.F) {
	for _, sequence := range [][]byte{{0, 0, 1, 3, 8, 2, 10, 0, 7, 0}, {0, 0, 1, 254, 6, 0, 4, 255}, {0, 0, 1, 5, 9, 0, 6, 0, 13, 0, 16, 0}} {
		f.Add(sequence)
	}
	f.Fuzz(func(t *testing.T, sequence []byte) {
		if len(sequence) > 512 {
			t.Skip()
		}
		advancedRunSequence(t, sequence)
	})
}
