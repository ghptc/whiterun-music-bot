//go:build stress

package player

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"whiterun/internal/media"
)

// The existing independent model checks semantics/order. This adds simultaneous
// pressure on real player goroutines, identity isolation and teardown ownership.
func TestEnduranceTwentyUsers(t *testing.T) {
	for _, seed := range []uint64{41, 20261019} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			type observation struct {
				User, Step, Guild int
				Operation         string
				ElapsedNS         int64
				Error             string
			}
			var mu sync.Mutex
			var trace []observation
			var active [2]atomic.Int64
			var overlaps atomic.Bool
			players := make([]*GuildPlayer, 2)
			for guild := range players {
				g := guild
				players[g], _ = newTestPlayer(t, func(ctx context.Context, _ media.Track, v Voice) error {
					if active[g].Add(1) != 1 {
						overlaps.Store(true)
					}
					defer active[g].Add(-1)
					_ = v.WriteFrame(ctx, []byte{1})
					<-ctx.Done()
					return ctx.Err()
				})
			}
			var workers sync.WaitGroup
			gate := make(chan struct{})
			for user := 0; user < 20; user++ {
				workers.Go(func() {
					rng := rand.New(rand.NewPCG(seed+uint64(user), 91))
					<-gate
					for step := 0; step < 100; step++ {
						guild := rng.IntN(2)
						p := players[guild]
						op := rng.IntN(15)
						var err error
						names := []string{"play", "skip", "pause", "resume", "previous", "remove", "move", "shuffle", "jump", "replay", "loop", "seek", "clear", "stop", "snapshot"}
						at := time.Now()
						switch op {
						case 0:
							id := fmt.Sprintf("guild%d/user%d/step%d", guild, user, step)
							err = p.Enqueue(media.Track{Title: id, SpotifyID: id, VideoID: "abcdefghijk", Artists: []string{id}}, p.Ticket())
						case 1:
							_, err = p.SkipN(rng.IntN(5))
						case 2:
							_, err = p.SetPaused(true)
						case 3:
							_, err = p.SetPaused(false)
						case 4:
							err = p.Previous()
						case 5:
							err = p.Remove(rng.IntN(30))
						case 6:
							err = p.Move(rng.IntN(30), rng.IntN(30))
						case 7:
							err = p.Shuffle()
						case 8:
							err = p.Jump(rng.IntN(30))
						case 9:
							err = p.Replay()
						case 10:
							err = p.SetLoop([]string{"off", "track", "queue", "invalid"}[rng.IntN(4)])
						case 11:
							err = p.Seek([]string{"0:00", "1:00", "bad"}[rng.IntN(3)])
						case 12:
							p.Clear()
						case 13:
							p.Stop()
						}
						elapsed := time.Since(at)
						state := p.State()
						tracks := append([]media.Track(nil), state.Queued...)
						if state.Current != nil {
							tracks = append(tracks, *state.Current)
						}
						if len(state.Entries) > MaxQueueTracks {
							t.Errorf("capacity exceeded seed=%d user=%d step=%d", seed, user, step)
						}
						for _, tr := range tracks {
							if !strings.HasPrefix(tr.SpotifyID, fmt.Sprintf("guild%d/", guild)) || tr.Title != tr.SpotifyID || len(tr.Artists) != 1 || tr.Artists[0] != tr.Title {
								t.Errorf("identity/isolation corrupted: %+v", tr)
							}
						}
						message := ""
						if err != nil {
							message = err.Error()
						}
						mu.Lock()
						trace = append(trace, observation{user, step, guild, names[op], elapsed.Nanoseconds(), message})
						mu.Unlock()
					}
				})
			}
			close(gate)
			workers.Wait()
			for i, p := range players {
				p.Close(context.Background())
				if active[i].Load() != 0 {
					t.Error("play goroutine survived Close")
				}
				select {
				case <-p.done:
				default:
					t.Error("run loop survived Close")
				}
			}
			if overlaps.Load() {
				t.Error("overlapping playback in one guild")
			}
			if dir := os.Getenv("BARD_QA_TRACE_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				b, err := json.MarshalIndent(map[string]any{"seed": seed, "users": 20, "operations": trace}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("twenty-users-%d.json", seed)), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("seed=%d users=20 guilds=2 operations=%d", seed, len(trace))
		})
	}
}
