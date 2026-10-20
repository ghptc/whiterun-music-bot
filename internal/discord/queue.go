package discord

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

// P0: every queue view fits one Discord message (40 components, 4000 UTF-16 units).
const queueBlockSize = 10

// A restrained aged-gold accent evokes Whiterun; Discord supplies the surface
// and text colors for the reader's theme. Use one accent per block, not per row.
const whiterunAccent = 0xA68B5B

type queueRow struct {
	Track    media.Track
	Position int
}

type queueBlock struct {
	Container   d.ContainerComponent
	Rows        []queueRow
	First, Last int // upcoming positions; current never consumes a position
}
type queueView struct {
	Blocks                            []queueBlock
	Shown, Total, Missing, Components int
}

func queueEntries(state player.State) []player.QueueEntry {
	if state.Entries != nil {
		return state.Entries
	}
	entries := make([]player.QueueEntry, len(state.Queued))
	for i, t := range state.Queued {
		entries[i] = player.QueueEntry{Track: t}
	}
	return entries
}
func queueClip(s string, maxUnits int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(utf16.Encode([]rune(s))) <= maxUnits {
		return s
	}
	units, n := 0, 0
	for i, r := range []rune(s) {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > maxUnits-1 {
			break
		}
		units += size
		n = i + 1
	}
	return string([]rune(s)[:n]) + "…"
}

// Escape provider text before applying a UTF-16 budget. Mentions never ping;
// labels cannot inject headings, links, or extra visual rows.
var musicEscaper = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "", "[", "\\[", "]", "\\]", "<", "", ">", "", "@", "@\u200b", "|", "\\|")

func musicLabel(s string, budget int) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if strings.HasPrefix(f, "https://") || strings.HasPrefix(f, "http://") {
			fields[i] = ""
		}
	}
	s = strings.Join(fields, " ")
	s = musicEscaper.Replace(s)
	return queueClip(s, budget)
}
func songSection(t media.Track, prefix, detail string) d.ContainerSubComponent {
	return styledSongSection(t, prefix, detail, true)
}

// Queue typography is independent of Now Playing and playback announcements.
func styledSongSection(t media.Track, prefix, detail string, subtextArtist bool) d.ContainerSubComponent {
	name := musicLabel(t.Title, 100)
	if name == "" {
		name = "Untitled"
	}
	artist := t.Artist
	if artist == "" {
		artist = t.Channel
	}
	artist = musicLabel(artist, 80)
	text := prefix + "**" + name + "**"
	if artist != "" {
		text += "\n"
		if subtextArtist {
			text += "-# "
		}
		text += artist
	}
	if detail != "" {
		text += "\n-# " + detail
	}
	art := media.ArtworkURL(t)
	description := "Artwork · " + name
	if art == "" {
		return d.TextDisplayComponent{Content: text}
	}
	return d.SectionComponent{Components: []d.SectionSubComponent{d.TextDisplayComponent{Content: text}}, Accessory: d.ThumbnailComponent{Media: d.UnfurledMediaItem{URL: art}, Description: description}}
}

// Build native components from one detached snapshot, without artwork I/O.
func buildQueueView(state player.State, limit *int) (queueView, error) {
	return buildQueueRange(state, limit, nil, nil)
}

func buildQueueRange(state player.State, limit, from, to *int) (queueView, error) {
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
		end = min(end, *to)
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
	units, components, artworks := 0, 3, 0 // container, heading, footer
	units = len(utf16.Encode([]rune("### The Bard · Queue")))
	currentOmitted := false
	footerFor := func(shown, first, last int) string {
		label := "Now playing"
		if last > 0 {
			label = fmt.Sprintf("%d–%d", first, last)
		}
		footer := fmt.Sprintf("-# %s · %d of %d songs up next · %d positions displayed · %d remaining", label, shown, view.Total, shown, view.Total-shown)
		if start <= end && shown < end-start+1 {
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
		illustrated := art != "" && artworks < queueBlockSize && compact == ""
		cost := 0
		if illustrated {
			cost = 3
		} else if compact == "" {
			cost = 1
		}
		size := len(utf16.Encode([]rune(text)))
		if !illustrated && compact != "" {
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
		if units+size+footerUnits > 4000 || components+cost > 40 {
			return false
		}
		units += size
		components += cost
		if illustrated {
			body = append(body, d.SectionComponent{Components: []d.SectionSubComponent{d.TextDisplayComponent{Content: text}}, Accessory: d.ThumbnailComponent{Media: d.UnfurledMediaItem{URL: art}, Description: "Artwork"}})
			artworks++
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
func musicContainer(heading string, body ...d.ContainerSubComponent) d.ContainerComponent {
	components := []d.ContainerSubComponent{d.TextDisplayComponent{Content: "### " + heading}}
	return d.ContainerComponent{AccentColor: whiterunAccent, Components: append(components, body...)}
}
func componentUpdate(container d.ContainerComponent) d.MessageUpdate {
	update := d.NewMessageUpdateV2(container)
	update.AllowedMentions = &d.AllowedMentions{}
	return update
}
func (block queueBlock) update(note string) d.MessageUpdate {
	container := block.Container
	if note != "" {
		container.Components = append([]d.ContainerSubComponent(nil), container.Components...)
		i := len(container.Components) - 1
		footer, ok := container.Components[i].(d.TextDisplayComponent)
		if ok {
			footer.Content += "\n-# " + note
			container.Components[i] = footer
		}
	}
	return componentUpdate(container)
}

type queueDelivery struct {
	cancel context.CancelFunc
	id     uint64
}

func (g *guild) beginQueueDelivery(parent context.Context, channel snowflake.ID) (context.Context, func()) {
	g.queueMu.Lock()
	defer g.queueMu.Unlock()
	if g.queueDeliveries == nil {
		g.queueDeliveries = make(map[snowflake.ID]queueDelivery)
	}
	if previous, ok := g.queueDeliveries[channel]; ok {
		previous.cancel()
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Minute)
	g.queueSequence++
	id := g.queueSequence
	g.queueDeliveries[channel] = queueDelivery{cancel, id}
	return ctx, func() {
		cancel()
		g.queueMu.Lock()
		defer g.queueMu.Unlock()
		if g.queueDeliveries[channel].id == id {
			delete(g.queueDeliveries, channel)
		}
	}
}
func (b *Bot) replyQueue(e *events.ApplicationCommandInteractionCreate, state player.State, limit *int) {
	started := time.Now()
	data := e.SlashCommandInteractionData()
	var from, to *int
	if value, ok := data.OptInt("from"); ok {
		from = &value
	}
	if value, ok := data.OptInt("to"); ok {
		to = &value
	}
	view, err := buildQueueRange(state, limit, from, to)
	b.log.Info("queue_plan", "interaction_id", e.ID(), "elapsed_us", time.Since(started).Microseconds(), "components", view.Components, "messages", len(view.Blocks), "shown", view.Shown, "total", view.Total, "artwork_missing", view.Missing)
	if err != nil {
		view.Blocks = []queueBlock{{Container: musicContainer("The Bard · Queue", d.TextDisplayComponent{Content: err.Error()})}}
	}
	ctx := b.ctx
	finish := func() {}
	if e.GuildID() != nil {
		if g := b.getGuild(*e.GuildID()); g != nil {
			channelID := snowflake.ID(0)
			if channel := e.Channel(); channel.MessageChannel != nil {
				channelID = channel.ID()
			}
			ctx, finish = g.beginQueueDelivery(b.ctx, channelID)
		}
	}
	defer finish()
	sendUpdate := func(update d.MessageUpdate, parent context.Context) error {
		bounded, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		sendStarted := time.Now()
		_, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), update, rest.WithCtx(bounded))
		b.log.Info("queue_response", "interaction_id", e.ID(), "elapsed_us", time.Since(sendStarted).Microseconds(), "error", err)
		return err
	}
	// P0: only PATCH @original. Never create a follow-up or continuation.
	err = sendUpdate(view.Blocks[0].update(""), ctx)
	if err != nil {
		b.log.Warn("queue_delivery_failed", "error", err)
		fallback, cancel := context.WithTimeout(b.ctx, 3*time.Second)
		defer cancel()
		notice := musicContainer("The Bard · Queue", d.TextDisplayComponent{Content: "Unable to display the queue. Try /queue again."})
		if failure := sendUpdate(componentUpdate(notice), fallback); failure != nil {
			b.log.Warn("queue_delivery_notice_failed", "error", failure)
		}
	}

}

func (block queueBlock) rangeLabel() string {
	if block.Last == 0 {
		return "Now playing"
	}
	return fmt.Sprintf("%d–%d", block.First, block.Last)
}
