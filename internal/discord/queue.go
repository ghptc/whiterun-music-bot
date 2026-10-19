package discord

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode/utf16"

	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

const queuePreviewSize = 10

type queueRow struct {
	Number                                                   int
	Current                                                  bool
	Title, Artist, Album, Requester, Status, Artwork, Source string
}
type queueView struct {
	Summary         string
	Content         string
	Embeds          []d.Embed
	HTML, Text      string
	GalleryDuration time.Duration
	Shown, Total    int // future positions only; current is always separate
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

func queueSummary(state player.State, shown, total int, limited bool) string {
	var out strings.Builder
	out.WriteString(currentStateText(state) + "\n")
	if total == 0 {
		out.WriteString("The queue is empty.\n")
	}
	count := total
	if state.Current != nil {
		count++
	}
	fmt.Fprintf(&out, "%d tracks remaining. %d total including current.", total, count)
	if limited {
		fmt.Fprintf(&out, " Showing %d of %d upcoming tracks.", shown, total)
	}
	if state.ProviderBlocked {
		out.WriteString("\nPlayback blocked by YouTube. Queue retained; use /resume to retry the same track.")
	}
	if state.Halted {
		out.WriteString("\nPlayback halted after 5 consecutive failures. Queue retained; use /resume or /play.")
	}
	if state.PersistenceError != "" {
		out.WriteString("\n" + state.PersistenceError)
	}
	for _, s := range state.Imports {
		fmt.Fprintf(&out, "\nLast import: %d matched; %d uncertain; %d rejected; %d unavailable; %d skipped.", s.Resolved, s.Uncertain, s.Failed-s.Uncertain, s.MetadataSkipped, s.Skipped)
	}
	if len(state.Resolving) > 0 {
		total, resolved, failed := 0, 0, 0
		for _, j := range state.Resolving {
			total += j.Total
			resolved += j.Resolved
			failed += j.Failed
		}
		fmt.Fprintf(&out, "\nResolving collections: %d/%d matched; %d failed. Pending entries are not playable yet.", resolved, total, failed)
	}
	return out.String()
}

// Bound native previews in UTF-16 units; exports retain complete metadata.
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
func queueRowFor(t media.Track) queueRow {
	if t.Artist == "" {
		t.Artist = t.Channel
	}
	return queueRow{Title: t.Title, Artist: t.Artist, Album: t.Album, Requester: t.RequestedBy, Artwork: media.ArtworkURL(t), Source: t.RequestedSource, Status: "ready"}
}

// buildQueueView performs no player, provider, file-system or network work.
func buildQueueView(state player.State, limit *int) (queueView, error) {
	if limit != nil && *limit < 1 {
		return queueView{}, fmt.Errorf("Queue limit must be at least 1.")
	}
	entries := queueEntries(state)
	shown := len(entries)
	if limit != nil {
		shown = min(shown, *limit)
	}
	v := queueView{Shown: shown, Total: len(entries)}
	v.Summary = queueSummary(state, shown, len(entries), limit != nil)
	v.Content = v.Summary
	rows := make([]queueRow, 0, shown+1)
	if state.Current != nil {
		row := queueRowFor(*state.Current)
		row.Current = true
		row.Status = string(state.PlaybackState)
		if row.Status == "" {
			row.Status = "playing"
		}
		if state.Paused {
			row.Status = "paused"
		}
		rows = append(rows, row)
	}
	for i, e := range entries[:shown] {
		row := queueRowFor(e.Track)
		row.Number = i + 1
		if e.Track.RequestedSource == "spotify" && e.Track.URL == "" {
			row.Status = "awaiting resolution"
		}
		if e.Pending {
			row.Status = "resolving"
		}
		if e.State == player.Resolving {
			row.Status = "resolving"
		}
		rows = append(rows, row)
	}
	for _, row := range rows[:min(len(rows), queuePreviewSize)] {
		heading := fmt.Sprintf("%d. %s", row.Number, row.Title)
		color := 0x5865f2
		if row.Current {
			heading = "▶ " + row.Title
			color = 0xd6ad60
		}
		description := queueClip(row.Artist, 100) + "\n" + row.Status
		if row.Requester != "" {
			description += "\nRequested by: " + queueClip(row.Requester, 80)
		}
		if row.Source == "spotify" {
			description += "\nSpotify metadata · YouTube audio"
		}
		embed := d.Embed{Title: queueClip(heading, 180), Description: description, Color: color}
		if row.Artwork != "" {
			embed.Thumbnail = &d.EmbedResource{URL: row.Artwork}
		}
		v.Embeds = append(v.Embeds, embed)
	}
	if len(rows) == 0 {
		return v, nil
	}
	v.Content += fmt.Sprintf("\n%d cards shown inline. Complete selected queue: queue.txt; artwork gallery: queue.html (download and open in a browser).", len(v.Embeds))
	var text strings.Builder
	text.WriteString(v.Summary + "\n\n")
	for _, row := range rows {
		label := fmt.Sprintf("%d", row.Number)
		if row.Current {
			label = "CURRENT"
		}
		fmt.Fprintf(&text, "%s. %s — %s [%s]\nAlbum: %s\nRequested by: %s\nSource: %s\nArtwork: %s\n\n", label, row.Artist, row.Title, row.Status, row.Album, row.Requester, row.Source, row.Artwork)
	}
	v.Text = text.String()
	var html strings.Builder
	galleryStarted := time.Now()
	if err := queueGallery.Execute(&html, struct {
		Summary string
		Rows    []queueRow
	}{v.Summary, rows}); err != nil {
		return v, fmt.Errorf("queue gallery: %w", err)
	}
	v.GalleryDuration = time.Since(galleryStarted)
	v.HTML = html.String()
	return v, nil
}

var queueGallery = template.Must(template.New("queue").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src https:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<meta name="referrer" content="no-referrer"><title>Bard · Queue</title>
<style>body{margin:0;background:#11151c;color:#e8e5de;font:16px system-ui}main{max-width:1100px;margin:auto;padding:32px}h1{color:#d6ad60}header p{white-space:pre-line;color:#aab4c4}ol{list-style:none;padding:0;display:grid;gap:12px}li{display:flex;align-items:center;gap:18px;padding:16px;background:#1c2431;border-radius:12px;overflow-wrap:anywhere}.current{border:2px solid #d6ad60}img,.placeholder{width:80px;height:80px;object-fit:cover;border-radius:8px;flex-shrink:0}.placeholder{display:grid;place-items:center;background:#303d52;font-size:28px}.meta{color:#aab4c4;font-size:14px}h2{margin:0 0 6px;font-size:18px}.number{color:#d6ad60}p{margin:4px 0}@media(max-width:500px){main{padding:16px}li{gap:10px;padding:12px}img,.placeholder{width:56px;height:56px}}</style>
<main><header><h1>♫ Bard · Queue</h1><p>{{.Summary}}</p><p>One snapshot · images load from their providers when opened · no playback controls.</p></header><ol>
{{range .Rows}}<li{{if .Current}} class="current"{{end}}>{{if .Artwork}}<img loading="lazy" referrerpolicy="no-referrer" src="{{.Artwork}}" alt="Album artwork">{{else}}<span class="placeholder" aria-label="No artwork">♫</span>{{end}}<div><h2><span class="number">{{if .Current}}▶ Current{{else}}{{.Number}}{{end}}</span> · {{.Title}}</h2><p>{{.Artist}}</p><p class="meta">{{.Album}} · {{.Status}}{{if .Source}} · {{.Source}}{{end}}</p>{{if .Requester}}<p class="meta">Requested by: {{.Requester}}</p>{{end}}</div></li>{{end}}
</ol></main></html>`))

func (v queueView) update(rich bool) d.MessageUpdate {
	embeds := []d.Embed{}
	files := []*d.File{}
	content := v.Content
	if rich {
		embeds = v.Embeds
		if v.HTML != "" {
			files = append(files, &d.File{Name: "queue.html", Reader: strings.NewReader(v.HTML)})
		}
	} else {
		content = v.Summary + "\nArtwork preview unavailable. The complete selected list is attached as queue.txt."
	}
	if v.Text != "" {
		files = append(files, &d.File{Name: "queue.txt", Reader: strings.NewReader(v.Text)})
	}
	return d.MessageUpdate{Content: &content, Embeds: &embeds, Attachments: &[]d.AttachmentUpdate{}, Files: files, AllowedMentions: &d.AllowedMentions{}}
}

// A single bounded multipart edit avoids per-track sends. disgo's REST limiter
// owns bucket waits/429 retries; the shared deadline bounds the entire delivery.
func (b *Bot) replyQueue(e *events.ApplicationCommandInteractionCreate, state player.State, limit *int) {
	started := time.Now()
	v, err := buildQueueView(state, limit)
	missing, invalid := 0, 0
	for _, entry := range queueEntries(state) {
		if media.ArtworkURL(entry.Track) == "" {
			missing++
		}
		if artworkInvalid(entry.Track) {
			invalid++
		}
	}
	b.log.Info("queue_render", "elapsed_ms", float64(time.Since(started).Microseconds())/1000, "gallery_elapsed_ms", float64(v.GalleryDuration.Microseconds())/1000, "html_bytes", len(v.HTML), "text_bytes", len(v.Text), "artwork_missing", missing, "artwork_metadata_invalid", invalid, "preview_cards", len(v.Embeds), "total", v.Total)
	defer func() {
		b.log.Info("queue_delivery_complete", "elapsed_ms", float64(time.Since(started).Microseconds())/1000)
	}()
	if err != nil {
		v.Content = err.Error()
	}
	budget := 8 << 20
	if advertised := e.AttachmentSizeLimit(); advertised > 0 {
		budget = min(budget, advertised)
	}
	rich := err == nil || v.Text == ""
	if err != nil {
		b.log.Warn("queue_render_failed", "error", err, "text_available", v.Text != "")
	}
	if len(v.HTML)+len(v.Text) > budget && len(v.Text) <= budget {
		rich = false
	}
	if len(v.Text) > budget {
		v = queueView{Content: "The queue export exceeds the attachment budget. No complete list was delivered. Use /queue limit with a smaller number."}
	}
	ctx, cancel := context.WithTimeout(b.ctx, 25*time.Second)
	defer cancel()
	send := func(update d.MessageUpdate) error {
		_, err := b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), update, rest.WithCtx(ctx))
		return err
	}
	if err = send(v.update(rich)); err == nil {
		return
	}
	b.log.Warn("queue rich delivery failed", "guild_id", e.GuildID(), "error", err)
	if ctx.Err() == nil && v.Text != "" {
		if err = send(v.update(false)); err == nil {
			return
		}
	}
	// Explicit failure replaces any partial/rich view; never claim completeness.
	failure := "The queue could not be delivered completely. Playback is unchanged. Try /queue again or /queue limit:10."
	fallbackCtx, fallbackCancel := context.WithTimeout(b.ctx, 3*time.Second)
	defer fallbackCancel()
	_, err = b.client.Rest.UpdateInteractionResponse(b.client.ApplicationID, e.Token(), d.MessageUpdate{Content: &failure, Embeds: &[]d.Embed{}, Attachments: &[]d.AttachmentUpdate{}, AllowedMentions: &d.AllowedMentions{}}, rest.WithCtx(fallbackCtx))
	if err != nil {
		b.log.Warn("queue failure notice could not be delivered", "guild_id", e.GuildID(), "error", err)
	}
}
