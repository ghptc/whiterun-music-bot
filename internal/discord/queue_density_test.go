package discord

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

type densityMetrics struct{ components, characters, images int }

// Validate the serialized wire payload, independently of renderer accounting.
// Only the component subset used by these queue layouts is accepted.
func validateDensity(raw []byte) (densityMetrics, error) {
	var payload struct {
		Flags      int              `json:"flags"`
		Components []map[string]any `json:"components"`
	}
	var m densityMetrics
	if err := json.Unmarshal(raw, &payload); err != nil {
		return m, err
	}
	if payload.Flags&(1<<15) == 0 || len(payload.Components) == 0 {
		return m, fmt.Errorf("missing V2 envelope")
	}
	var walk func(map[string]any, int) error
	walk = func(c map[string]any, parent int) error {
		m.components++
		typ, ok := c["type"].(float64)
		if !ok {
			return fmt.Errorf("missing type")
		}
		kind := int(typ)
		children, _ := c["components"].([]any)
		switch kind {
		case 17:
			if parent != 0 || len(children) == 0 {
				return fmt.Errorf("invalid container nesting")
			}
		case 9:
			if parent != 0 && parent != 17 {
				return fmt.Errorf("invalid section nesting")
			}
			if len(children) < 1 || len(children) > 3 {
				return fmt.Errorf("section cardinality")
			}
			accessory, ok := c["accessory"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing accessory")
			}
			if err := walk(accessory, 9); err != nil {
				return err
			}
		case 10:
			if parent != 0 && parent != 17 && parent != 9 {
				return fmt.Errorf("invalid text nesting")
			}
			content, ok := c["content"].(string)
			if !ok || content == "" {
				return fmt.Errorf("empty text")
			}
			m.characters += queueUnits(content)
		case 11:
			if parent != 9 {
				return fmt.Errorf("thumbnail outside section")
			}
			mediaValue, ok := c["media"].(map[string]any)
			if !ok {
				return fmt.Errorf("missing media")
			}
			url, _ := mediaValue["url"].(string)
			if media.SafeArtworkURL(url) == "" {
				return fmt.Errorf("invalid artwork")
			}
			description, _ := c["description"].(string)
			if queueUnits(description) > 1024 {
				return fmt.Errorf("alt text budget")
			}
			m.images++
		case 14:
			if parent != 0 && parent != 17 {
				return fmt.Errorf("separator nesting")
			}
		default:
			return fmt.Errorf("unsupported component %d", kind)
		}
		for _, child := range children {
			value, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid child")
			}
			if kind == 9 && value["type"] != float64(10) {
				return fmt.Errorf("section child must be Text Display")
			}
			if err := walk(value, kind); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range payload.Components {
		if err := walk(c, 0); err != nil {
			return m, err
		}
	}
	if m.components > 40 || m.characters > 4000 {
		return m, fmt.Errorf("message budget: %+v", m)
	}
	return m, nil
}

func densityFixture(n int, mixed bool) player.State {
	state := queueFixture(n)
	current := state.Entries[0].Track
	current.Title = "Now playing · Água 日本語 🎵"
	current.Duration = 3671 * time.Second
	state.Current = &current
	for i := range state.Entries {
		tr := &state.Entries[i].Track
		tr.Duration = time.Duration(180+i%400) * time.Second
		if mixed {
			if i%7 == 0 {
				tr.Title = strings.Repeat("Água 日本語 🎵 é ", 8)
			}
			if i%5 == 0 {
				tr.SpotifyArtworkURL = ""
			}
			if i%9 == 0 {
				tr.Duration = 0
			}
		}
	}
	return state
}

// Run with go test ./internal/discord -run '^TestQueueDensitySimulation$' -v.
// Construction/payloads are deterministic; timing/allocation observations vary by host.
func TestQueueDensitySimulation(t *testing.T) {
	variants := []struct {
		name  string
		build func(player.State) (queueView, error)
	}{
		{"A-current", func(s player.State) (queueView, error) { return buildQueueBaseline(s, nil, nil, nil) }},
		{"B-sections", func(s player.State) (queueView, error) { return buildQueueLayout(s, nil, nil, nil, false) }},
		{"C-optimized", func(s player.State) (queueView, error) { return buildQueueView(s, nil) }},
	}
	t.Log("profile | queued | layout | shown upcoming | images incl current | components | UTF16 text | JSON bytes | render ns/op | marshal ns/op | render allocs/op | marshal allocs/op | stopping budget")
	for _, mixed := range []bool{false, true} {
		profile := "short"
		if mixed {
			profile = "mixed"
		}
		for _, n := range []int{10, 100, 300, 1000} {
			state := densityFixture(n, mixed)
			for _, v := range variants {
				view, err := v.build(state)
				if err != nil {
					t.Fatal(err)
				}
				update := view.Blocks[0].update("")
				raw, err := json.Marshal(update)
				if err != nil {
					t.Fatal(err)
				}
				m, err := validateDensity(raw)
				if err != nil {
					t.Fatal(v.name, err)
				}
				if m.components != view.Components {
					t.Fatal("accounting drift", v.name, m, view.Components)
				}
				// Also check input metadata and serialized visual order, not just row bookkeeping.
				if v.name != "A-current" {
					text := viewText(view)
					previous := -1
					for _, row := range view.Blocks[0].Rows {
						prefix := fmt.Sprintf("%d · **", row.Position)
						if row.Position == 0 {
							prefix = "Now playing · **"
						}
						at := strings.Index(text, prefix)
						if at <= previous {
							t.Fatal("visual order", v.name, row.Position)
						}
						previous = at
					}
				}
				const runs = 200
				renderAllocs := testing.AllocsPerRun(runs, func() {
					if _, err := v.build(state); err != nil {
						panic(err)
					}
				})
				marshalAllocs := testing.AllocsPerRun(runs, func() {
					if _, err := json.Marshal(update); err != nil {
						panic(err)
					}
				})
				started := time.Now()
				for range runs {
					_, _ = v.build(state)
				}
				renderNS := time.Since(started).Nanoseconds() / runs
				started = time.Now()
				for range runs {
					_, _ = json.Marshal(update)
				}
				marshalNS := time.Since(started).Nanoseconds() / runs
				stop := "selection"
				if view.Shown < n {
					stop = "text"
					if v.name == "B-sections" {
						stop = "components"
					}
				}
				t.Logf("%s | %d | %s | %d | %d | %d | %d | %d | %d | %d | %.0f | %.0f | %s", profile, n, v.name, view.Shown, m.images, m.components, m.characters, len(raw), renderNS, marshalNS, renderAllocs, marshalAllocs, stop)
			}
		}
	}
}

func TestQueueDensityCurrentMetadataAndDuration(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{{475 * time.Second, "07:55"}, {3671 * time.Second, "01:01:11"}, {0, "Duration unavailable"}, {-1, "Duration unavailable"}} {
		state := densityFixture(10, false)
		state.Current.Duration = tc.duration
		state.Current.YouTubeThumbnailURL = "https://i.ytimg.com/fallback"
		view, err := buildQueueView(state, nil)
		if err != nil {
			t.Fatal(err)
		}
		section, ok := view.Blocks[0].Container.Components[1].(d.SectionComponent)
		if !ok || section.Accessory.(d.ThumbnailComponent).Media.URL != state.Current.SpotifyArtworkURL {
			t.Fatal("current Spotify artwork lost")
		}
		text := section.Components[0].(d.TextDisplayComponent).Content
		if !strings.Contains(text, "\n-# Artist\n-# "+tc.want) {
			t.Fatal(text)
		}
		state.Current.SpotifyArtworkURL = "invalid"
		view, _ = buildQueueView(state, nil)
		section = view.Blocks[0].Container.Components[1].(d.SectionComponent)
		if section.Accessory.(d.ThumbnailComponent).Media.URL != state.Current.YouTubeThumbnailURL {
			t.Fatal("current YouTube fallback lost")
		}
	}
}

func TestQueueDensityValidatorRejectsMalformedPayloads(t *testing.T) {
	for _, raw := range []string{
		`{"flags":32768,"components":[{"type":11,"media":{"url":"https://art.example/a"}}]}`,
		`{"flags":32768,"components":[{"type":17,"components":[{"type":17,"components":[{"type":10,"content":"nested"}]}]}]}`,
		`{"flags":32768,"components":[{"type":9,"components":[],"accessory":{"type":11,"media":{"url":"https://art.example/a"}}}]}`,
	} {
		if _, err := validateDensity([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	components := make([]d.LayoutComponent, 41)
	for i := range components {
		components[i] = d.TextDisplayComponent{Content: "x"}
	}
	oversized, _ := json.Marshal(d.NewMessageUpdateV2(components...))
	if _, err := validateDensity(oversized); err == nil {
		t.Fatal("accepted component overflow")
	}
	state := densityFixture(100, false)
	v, _ := buildQueueView(state, nil)
	container := v.Blocks[0].Container
	container.Components = append(container.Components, d.TextDisplayComponent{Content: strings.Repeat("x", 4001)})
	raw, _ := json.Marshal(componentUpdate(container))
	if _, err := validateDensity(raw); err == nil {
		t.Fatal("accepted budget overflow")
	}
}
