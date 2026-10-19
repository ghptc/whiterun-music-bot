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

// One container + heading + separator + footer leaves 36 components for
// twelve sections (each section, its text display, and thumbnail count).
const queueBlockSize = 12
const fallbackArtwork = "https://cdn.discordapp.com/embed/avatars/1.png"

type queueBlock struct {
	Container   d.ContainerComponent
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
func songSection(t media.Track, prefix, detail string) d.SectionComponent {
	name := musicLabel(t.Title, 100)
	if name == "" {
		name = "Untitled"
	}
	artist := t.Artist
	if artist == "" {
		artist = t.Channel
	}
	artist = musicLabel(artist, 80)
	text := "**" + prefix + name + "**"
	if artist != "" {
		text += "\n" + artist
	}
	if detail != "" {
		text += "\n-# " + detail
	}
	art := media.ArtworkURL(t)
	description := "Artwork · " + name
	if art == "" {
		art = fallbackArtwork
		description = "No artwork available"
	}
	return d.SectionComponent{Components: []d.SectionSubComponent{d.TextDisplayComponent{Content: text}}, Accessory: d.ThumbnailComponent{Media: d.UnfurledMediaItem{URL: art}, Description: description}}
}

// Pure render: all metadata comes from one detached snapshot. No catalog reads,
// image downloads, resolver calls, filesystem operations, or player locks.
func buildQueueView(state player.State, limit *int) (queueView, error) {
	if limit != nil && *limit < 1 {
		return queueView{}, fmt.Errorf("Queue limit must be at least 1.")
	}
	entries := queueEntries(state)
	shown := len(entries)
	if limit != nil {
		shown = min(shown, *limit)
	}
	view := queueView{Shown: shown, Total: len(entries)}
	rows := make([]d.ContainerSubComponent, 0, shown+1)
	if state.Current != nil {
		rows = append(rows, songSection(*state.Current, "", "Now playing"))
		if media.ArtworkURL(*state.Current) == "" {
			view.Missing++
		}
	}
	for i, e := range entries[:shown] {
		rows = append(rows, songSection(e.Track, fmt.Sprintf("%d. ", i+1), ""))
		if media.ArtworkURL(e.Track) == "" {
			view.Missing++
		}
	}
	if len(rows) == 0 {
		view.Blocks = []queueBlock{{Container: musicContainer("THE BARD · QUEUE", d.TextDisplayComponent{Content: "Nothing playing. The queue is empty."})}}
		view.Components = 4
		return view, nil
	}
	count := (len(rows) + queueBlockSize - 1) / queueBlockSize
	for offset := 0; offset < len(rows); offset += queueBlockSize {
		end := min(offset+queueBlockSize, len(rows))
		first, last := offset+1, end
		if state.Current != nil {
			first = max(1, first-1)
			last--
		}
		if last == 0 {
			first = 0
		}
		heading := fmt.Sprintf("### THE BARD · QUEUE\n-# %d songs up next", shown)
		if shown < len(entries) {
			heading = fmt.Sprintf("### THE BARD · QUEUE\n-# %d of %d songs up next", shown, len(entries))
		}
		components := []d.ContainerSubComponent{d.TextDisplayComponent{Content: heading}, d.SeparatorComponent{Divider: new(false), Spacing: d.SeparatorSpacingSizeSmall}}
		components = append(components, rows[offset:end]...)
		footer := fmt.Sprintf("-# Block %d/%d", len(view.Blocks)+1, count)
		if last > 0 {
			footer = fmt.Sprintf("-# %d–%d · Block %d/%d", first, last, len(view.Blocks)+1, count)
		}
		components = append(components, d.TextDisplayComponent{Content: footer})
		view.Blocks = append(view.Blocks, queueBlock{Container: d.ContainerComponent{Components: components}, First: first, Last: last})
		view.Components += 4 + 3*(end-offset)
	}
	return view, nil
}
func musicContainer(heading string, body ...d.ContainerSubComponent) d.ContainerComponent {
	components := []d.ContainerSubComponent{d.TextDisplayComponent{Content: "### " + heading}, d.SeparatorComponent{Divider: new(false), Spacing: d.SeparatorSpacingSizeSmall}}
	return d.ContainerComponent{Components: append(components, body...)}
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
	view, err := buildQueueView(state, limit)
	b.log.Info("queue_render", "elapsed_us", time.Since(started).Microseconds(), "components", view.Components, "messages", len(view.Blocks), "shown", view.Shown, "total", view.Total, "artwork_missing", view.Missing)
	if err != nil {
		view.Blocks = []queueBlock{{Container: musicContainer("THE BARD · QUEUE", d.TextDisplayComponent{Content: err.Error()})}}
	}
	// A user-only installation has a five-followup ceiling. Do not silently cut
	// a full queue down to that ceiling or switch to channel posts/exports.
	owners := e.AuthorizingIntegrationOwners()
	if _, userOnly := owners[d.ApplicationIntegrationTypeUserInstall]; userOnly && len(owners) == 1 && len(view.Blocks) > 6 {
		view.Blocks = []queueBlock{{Container: musicContainer("THE BARD · QUEUE", d.TextDisplayComponent{Content: "This queue needs more messages than a personal installation permits. Install the Bard in this server to view the full queue, or request /queue limit:50. No songs were omitted from a delivered list."})}}
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
		_, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), update, rest.WithCtx(bounded))
		return err
	}
	delivered := 0
	sent := 0
	initialNote := ""
	if len(view.Blocks) > 1 {
		initialNote = fmt.Sprintf("Sending %d blocks…", len(view.Blocks))
	}
	err = sendUpdate(view.Blocks[0].update(initialNote), ctx)
	if err == nil {
		sent++
		delivered = view.Blocks[0].Last
	}
	for i := 1; err == nil && i < len(view.Blocks); i++ {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
		message := d.NewMessageCreateV2(view.Blocks[i].Container)
		message.AllowedMentions = &d.AllowedMentions{}
		_, err = b.client.Rest.CreateFollowupMessage(b.client.ApplicationID, e.Token(), message, rest.WithCtx(bounded))
		cancel()
		if err == nil {
			sent++
			delivered = view.Blocks[i].Last
		}
	}
	if err != nil {
		b.log.Warn("queue_delivery_failed", "guild_id", e.GuildID(), "messages_sent", sent, "last_position", delivered, "error", err)
		notice := fmt.Sprintf("Delivery incomplete · %d/%d upcoming songs confirmed. Try /queue again.", delivered, view.Shown)
		if ctx.Err() == context.Canceled && b.ctx.Err() == nil {
			notice = "Delivery stopped · a newer /queue request replaced this view."
		}
		fallback, cancel := context.WithTimeout(b.ctx, 3*time.Second)
		defer cancel()
		if failure := sendUpdate(view.Blocks[0].update(notice), fallback); failure != nil {
			b.log.Warn("queue_delivery_notice_failed", "error", failure)
		}
	} else if len(view.Blocks) > 1 {
		if failure := sendUpdate(view.Blocks[0].update(""), ctx); failure != nil {
			b.log.Warn("queue_completion_notice_failed", "error", failure)
		}
	}
	b.log.Info("queue_delivery_complete", "elapsed_ms", time.Since(started).Milliseconds(), "messages_sent", sent, "positions_sent", delivered, "success", err == nil)
}
