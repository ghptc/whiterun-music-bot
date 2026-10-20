package discord

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
)

// Read-only Discord API capture, 2026-10-20. IDs and versions are server fields;
// required is omitted by Discord for all three optional integer options.
func TestCR007PublishedQueueContract(t *testing.T) {
	const captured = `{"id":"1548720833114153074","application_id":"1548718672984539166","version":"1562070939611439114","type":1,"name":"queue","description":"Show the current song and queue.","contexts":[0],"options":[{"type":4,"name":"limit","description":"Maximum upcoming tracks to show in one message","min_value":1},{"type":4,"name":"from","description":"First upcoming position (inclusive)","min_value":1},{"type":4,"name":"to","description":"Last upcoming position (inclusive)","min_value":1}]}`
	var published d.SlashCommand
	if err := json.Unmarshal([]byte(captured), &published); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []snowflake.ID{0, 1} {
		for _, command := range scopedCommands(scope) {
			if command.CommandName() != "queue" {
				continue
			}
			want := command.(d.SlashCommandCreate)
			got := published
			if !commandMatches(want, got) {
				t.Fatal("published/local mismatch", scope)
			}
			if len(want.Options) != 3 {
				t.Fatal(want.Options)
			}
			for i, name := range []string{"limit", "from", "to"} {
				option, ok := want.Options[i].(d.ApplicationCommandOptionInt)
				if !ok || option.Name != name || option.Required {
					t.Fatal(option)
				}
			}
			// Readback must detect stale required options, reordered options and types.
			for _, raw := range []string{
				strings.Replace(captured, `"name":"limit"`, `"name":"limit","required":true`, 1),
				strings.Replace(captured, `"name":"from"`, `"name":"old_from"`, 1),
				strings.Replace(captured, `"type":4`, `"type":3`, 1),
			} {
				var stale d.SlashCommand
				if err := json.Unmarshal([]byte(raw), &stale); err != nil {
					t.Fatal(err)
				}
				if commandMatches(want, stale) {
					t.Fatal("stale contract accepted")
				}
			}
		}
	}
}

func TestCR007QueueDefaultsRangesAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name            string
		limit, from, to *int
		first, shown    int
	}{
		{name: "default", first: 1},
		{name: "inclusive", from: new(60), to: new(70), first: 60, shown: 11},
		{name: "from", from: new(60), first: 60},
		{name: "limit", limit: new(25), first: 1, shown: 25},
		{name: "combined", limit: new(5), from: new(60), to: new(70), first: 60, shown: 5},
		{name: "to", to: new(8), first: 1, shown: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := queueFixture(300)
			state.Current = &media.Track{Title: "Atual 🎵", VideoID: "abcdefghijk"}
			view, err := buildQueueRange(state, tc.limit, tc.from, tc.to)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Blocks) != 1 || (tc.shown > 0 && view.Shown != tc.shown) || (tc.shown == 0 && view.Shown <= 10) {
				t.Fatal(view)
			}
			rows := view.Blocks[0].Rows
			if rows[0].Position != 0 || rows[1].Position != tc.first {
				t.Fatal(rows)
			}
			for i, row := range rows[1:] {
				if row.Position != tc.first+i {
					t.Fatal(row)
				}
			}
			text := viewText(view)
			if !strings.Contains(text, fmt.Sprintf("%d positions displayed · %d remaining", view.Shown, 300-view.Shown)) || !strings.Contains(text, fmt.Sprintf("Next position: %d", tc.first+view.Shown)) {
				t.Fatal(text)
			}
			art := 0
			for _, component := range view.Blocks[0].Container.Components {
				if _, ok := component.(d.SectionComponent); ok {
					art++
				}
			}
			if art != min(12, view.Shown+1) {
				t.Fatal("upcoming artwork", art)
			}
			assertV2Budget(t, view.Blocks[0].Container)
			// Default/from must stop only when the next full row plus footer cannot fit.
			if tc.shown == 0 {
				next := state.Entries[tc.first+view.Shown-1].Track
				nextText := fmt.Sprintf("\n\n%d · **%s**\n-# %s\n-# %s", tc.first+view.Shown, next.Title, next.Artist, queueDuration(next.Duration))
				actual := 0
				for _, c := range view.Blocks[0].Container.Components {
					switch v := c.(type) {
					case d.TextDisplayComponent:
						actual += len(utf16.Encode([]rune(v.Content)))
					case d.SectionComponent:
						for _, child := range v.Components {
							actual += len(utf16.Encode([]rune(child.(d.TextDisplayComponent).Content)))
						}
					}
				}
				if actual+len(utf16.Encode([]rune(nextText))) <= 4000 {
					t.Fatal("stopped before budget", actual, view.Shown)
				}
			}
		})
	}
	for _, tc := range []struct{ limit, from, to *int }{{from: new(301)}, {to: new(301)}, {from: new(70), to: new(60)}, {limit: new(0)}, {from: new(0)}, {to: new(-1)}} {
		if _, err := buildQueueRange(queueFixture(300), tc.limit, tc.from, tc.to); err == nil {
			t.Fatal("invalid accepted", tc)
		}
	}
}

func TestCR007ArtworkAfterMissingAndWholeUnicode(t *testing.T) {
	state := queueFixture(300)
	state.Entries[0].Track.SpotifyArtworkURL = ""
	state.Entries[1].Track.Title = "Água 日本語 🎵 e\u0301"
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	art := 0
	for _, c := range view.Blocks[0].Container.Components {
		if _, ok := c.(d.SectionComponent); ok {
			art++
		}
	}
	if art != 11 || !strings.Contains(viewText(view), state.Entries[1].Track.Title) {
		t.Fatal(art, viewText(view))
	}
	state.Current = &media.Track{Title: strings.Repeat("🎵", 2200)}
	view, err = buildQueueView(state, nil)
	if err != nil || !strings.Contains(viewText(view), "Current song omitted") {
		t.Fatal(err, viewText(view))
	}
	assertV2Budget(t, view.Blocks[0].Container)
}
