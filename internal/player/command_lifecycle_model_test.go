package player

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

type qa013ModelStore struct {
	mu  sync.Mutex
	raw []byte
}

func (s *qa013ModelStore) SaveGuild(_ context.Context, _ string, state DurableState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	s.raw, err = json.Marshal(state)
	return err
}
func (s *qa013ModelStore) LoadGuild(context.Context, string) (DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state DurableState
	if len(s.raw) == 0 {
		return state, nil
	}
	err := json.Unmarshal(s.raw, &state)
	return state, err
}
func qa013CompareDurable(t *testing.T, store *qa013ModelStore, m *advancedModel, step int) {
	t.Helper()
	s, err := store.LoadGuild(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	current := ""
	if s.Current != nil {
		current = s.Current.Title
	}
	if current != m.current || !slices.Equal(advancedIDs(s.Queue), m.queue) || !slices.Equal(advancedIDs(s.History), m.history) || s.Loop != m.loop || s.Autoplay != m.autoplay || s.CurrentStarted != (m.current != "") {
		t.Fatalf("step=%d durable state differs: %+v model=%+v", step, s, m)
	}
}
func TestQA013DurableModelSequences(t *testing.T) {
	for seed := uint64(0); seed < 256; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 0x5141303133))
			sequence := []byte{0, 0, 1, 5}
			for range 256 {
				sequence = append(sequence, byte(rng.IntN(18)), byte(rng.IntN(256)))
			}
			defer func() {
				if t.Failed() {
					dir := os.Getenv("BARD_QA_FAILURE_DIR")
					if dir == "" {
						dir = filepath.Join(os.TempDir(), "bard-qa013-failures")
					}
					_ = os.MkdirAll(dir, 0700)
					body, _ := json.MarshalIndent(map[string]any{"seed": seed, "hex": hex.EncodeToString(sequence)}, "", "  ")
					_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("durable-%d.json", seed)), body, 0600)
					t.Logf("replay: go test ./internal/player -run '^TestQA013DurableModelSequences$/seed=%d$' -count=1", seed)
				}
			}()
			advancedRunSequenceMode(t, sequence, true)
		})
	}
}
