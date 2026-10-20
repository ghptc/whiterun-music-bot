// Frozen pre-UI-015 renderer for deterministic before/after measurements.
package discord

import (
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"strings"
	"unicode/utf16"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

const queueBlockSize = 10

func buildQueueBaseline(state player.State, limit, from, to *int) (queueView, error) {
	for _, value := range []*int{limit, from, to} {
		if value != nil && *value < 1 {
			return queueView{}, fmt.Errorf("Queue arguments must be at least 1.")
		}
	}
	entries := queueEntries(state)
	start, end := 1, len(entries)
	if from != nil {
		start = *from
	}
	if to != nil {
		end = *to
	}
	if (from != nil && start > len(entries)) || (to != nil && end > len(entries)) {
		return queueView{}, fmt.Errorf("Queue positions must be within 1–%d.", len(entries))
	}
	if to != nil && *to < start {
		return queueView{}, fmt.Errorf("Queue to must be at least from.")
	}
	if limit != nil && start <= end && *limit < end-start+1 {
		end = start + *limit - 1
	}
	view := queueView{Total: len(entries)}
	block := queueBlock{}
	body := []d.ContainerSubComponent{}
	// Budget each candidate together with its complete footer before adding it.
	units, components := 0, 3 // container, heading, footer
	units = len(utf16.Encode([]rune("### The Bard · Queue")))
	currentOmitted := false
	footerFor := func(shown, first, last int) string {
		label := "Now playing"
		if last > 0 {
			label = fmt.Sprintf("%d–%d", first, last)
		}
		footer := fmt.Sprintf("-# %s · %d of %d songs up next · %d positions displayed · %d remaining", label, shown, view.Total, shown, view.Total-shown)
		if start+shown <= view.Total {
			footer += fmt.Sprintf(" · Next position: %d", start+shown)
		}
		if currentOmitted {
			footer += " · Current song omitted: exceeds message budget."
		}
		return footer
	}
	compact := ""
	add := func(row queueRow) bool {
		title := musicEscaper.Replace(strings.Join(strings.Fields(row.Track.Title), " "))
		if title == "" {
			title = "Untitled"
		}
		artist := row.Track.Artist
		if artist == "" {
			artist = row.Track.Channel
		}
		artist = musicEscaper.Replace(strings.Join(strings.Fields(artist), " "))
		prefix := fmt.Sprintf("%d · ", row.Position)
		if row.Position == 0 {
			prefix = "Now playing · "
		}
		text := prefix + "**" + title + "**"
		if artist != "" {
			text += " · " + artist
		}
		art := media.ArtworkURL(row.Track)
		preview := row.Position == 0 || view.Shown < queueBlockSize
		illustrated := art != "" && row.Position > 0 && preview
		cost := 0
		if illustrated {
			cost = 3
		} else if preview || compact == "" {
			cost = 1
		}
		size := len(utf16.Encode([]rune(text)))
		if !preview && compact != "" {
			size++
		}
		shown, first, last := view.Shown, block.First, block.Last
		if row.Position > 0 {
			if shown == 0 {
				first = row.Position
			}
			shown++
			last = row.Position
		}
		footerUnits := len(utf16.Encode([]rune(footerFor(shown, first, last))))
		if units+size+footerUnits > queueTextBudget || components+cost > queueComponentBudget {
			return false
		}
		units += size
		components += cost
		if illustrated {
			body = append(body, d.SectionComponent{Components: []d.SectionSubComponent{d.TextDisplayComponent{Content: text}}, Accessory: d.ThumbnailComponent{Media: d.UnfurledMediaItem{URL: art}, Description: "Artwork"}})
		} else if preview {
			body = append(body, d.TextDisplayComponent{Content: text})
		} else {
			compact += text + "\n"
		}
		if art == "" {
			view.Missing++
		}
		block.Rows = append(block.Rows, row)
		if row.Position > 0 {
			if view.Shown == 0 {
				block.First = row.Position
			}
			block.Last = row.Position
			view.Shown++
		}
		return true
	}
	currentOmitted = state.Current != nil && !add(queueRow{Track: *state.Current})
	for pos := start; pos <= end; pos++ {
		if !add(queueRow{Track: entries[pos-1].Track, Position: pos}) {
			break
		}
	}
	if compact != "" {
		body = append(body, d.TextDisplayComponent{Content: strings.TrimSuffix(compact, "\n")})
	}
	footer := footerFor(view.Shown, block.First, block.Last)
	if len(entries) == 0 && state.Current == nil {
		body = append(body, d.TextDisplayComponent{Content: "Nothing playing. The queue is empty."})
		components++
	}
	body = append(body, d.TextDisplayComponent{Content: footer})
	block.Container = musicContainer("The Bard · Queue", body...)
	view.Blocks = []queueBlock{block}
	view.Components = components
	return view, nil
}
