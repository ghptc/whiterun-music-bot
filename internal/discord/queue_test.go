package discord

import (
	"context"
	"encoding/json"
	"fmt"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"testing"
	"unicode/utf16"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func componentText(components []d.LayoutComponent) string {
	var out strings.Builder
	var walk func(d.Component)
	walk = func(c d.Component) {
		switch v := c.(type) {
		case d.ContainerComponent:
			for _, child := range v.Components {
				walk(child)
			}
		case d.MediaGalleryComponent:
			for _, item := range v.Items {
				out.WriteString(item.Description + "\n")
			}
		case d.SectionComponent:
			for _, child := range v.Components {
				walk(child)
			}
		case d.TextDisplayComponent:
			out.WriteString(v.Content + "\n")
		}
	}
	for _, c := range components {
		walk(c)
	}
	return out.String()
}
func updateText(update d.MessageUpdate) string {
	if update.Components != nil {
		return componentText(*update.Components)
	}
	if update.Content != nil {
		return *update.Content
	}
	return ""
}
func messageText(message d.MessageCreate) string {
	return message.Content + componentText(message.Components)
}
func viewText(view queueView) string {
	var out strings.Builder
	for _, block := range view.Blocks {
		out.WriteString(componentText([]d.LayoutComponent{block.Container}))
	}
	return out.String()
}
func queueSections(view queueView) []queueRow {
	var rows []queueRow
	for _, block := range view.Blocks {
		rows = append(rows, block.Rows...)
	}
	return rows
}
func queueFixture(n int) player.State {
	s := player.State{}
	for i := range n {
		s.Entries = append(s.Entries, player.QueueEntry{Track: media.Track{Title: fmt.Sprintf("Song %04d", i+1), Artist: "Artist", SpotifyID: fmt.Sprint(i), SpotifyArtworkURL: fmt.Sprintf("https://art.example/album%d", i), RequestedBy: "listener"}, Pending: i%2 == 0})
	}
	return s
}
func assertV2Budget(t *testing.T, container d.ContainerComponent) {
	t.Helper()
	raw, err := json.Marshal(container)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	count, text := 0, 0
	var walk func(map[string]any)
	walk = func(c map[string]any) {
		count++
		typ := int(c["type"].(float64))
		if typ == 10 {
			text += len(utf16.Encode([]rune(c["content"].(string))))
		}
		if typ != 12 && typ != 17 && typ != 14 && typ != 9 && typ != 10 && typ != 11 {
			t.Fatal("unexpected component", typ)
		}
		if children, ok := c["components"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
		if accessory, ok := c["accessory"].(map[string]any); ok {
			walk(accessory)
		}
	}
	walk(value)
	if count > 40 || text > 4000 {
		t.Fatal("V2 budget", count, text)
	}
}
func TestQueueCompleteAndLimitedSnapshots(t *testing.T) {
	for _, size := range []int{0, 1, 10, 123, 500, 1000} {
		for _, limit := range []*int{nil, new(1), new(10), new(50), new(2000)} {
			t.Run(fmt.Sprintf("%d/%v", size, limit), func(t *testing.T) {
				state := queueFixture(size)
				view, err := buildQueueView(state, limit)
				if err != nil {
					t.Fatal(err)
				}
				want := size
				if limit != nil {
					want = min(want, *limit)
				}
				rows := queueSections(view)
				if view.Shown != want || view.Total != size || len(rows) != want {
					t.Fatal(view.Shown, view.Total, len(rows), want)
				}
				for i, row := range rows {
					if row.Position != i+1 || row.Track.Title != fmt.Sprintf("Song %04d", i+1) || row.Track.SpotifyArtworkURL != state.Entries[i].Track.SpotifyArtworkURL {
						t.Fatal("order/metadata", row)
					}
				}
				expectedBlocks := max(1, (want+11)/12)
				if len(view.Blocks) != expectedBlocks {
					t.Fatal("message count", len(view.Blocks), expectedBlocks)
				}
				for _, block := range view.Blocks {
					assertV2Budget(t, block.Container)
					update := block.update("")
					if len(update.Files) > 0 || update.Embeds != nil || update.Content != nil || update.Flags == nil || *update.Flags&d.MessageFlagIsComponentsV2 == 0 || len(*update.Components) != 1 {
						t.Fatal("invalid V2 message", update)
					}
				}
				if size == 0 && !strings.Contains(viewText(view), "queue is empty") {
					t.Fatal(viewText(view))
				}
			})
		}
	}
}
func TestQueueCurrentPendingAndArtwork(t *testing.T) {
	state := queueFixture(123)
	state.Current = &media.Track{Title: "Current", Artist: "Current artist", YouTubeThumbnailURL: "https://i.ytimg.com/current"}
	state.Paused = true
	state.Entries[1].Track = media.Track{Title: "Missing", SpotifyArtworkURL: "javascript:bad"}
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := queueSections(view)
	if len(rows) != 124 || rows[0].Track.Title != "Current" {
		t.Fatal("current duplicated/omitted")
	}
	if rows[0].Track.YouTubeThumbnailURL != state.Current.YouTubeThumbnailURL || rows[1].Position != 1 || media.ArtworkURL(rows[2].Track) != "" || view.Missing != 1 {
		t.Fatal("artwork policy")
	}

	for _, forbidden := range []string{"ready", "pending", "PLAYING", "Spotify metadata", "YouTube audio", "listener", "resolving"} {
		if strings.Contains(viewText(view), forbidden) {
			t.Fatal(forbidden)
		}
	}
	view, err = buildQueueView(player.State{Current: state.Current}, new(1))
	if err != nil || len(queueSections(view)) != 1 {
		t.Fatal(view, err)
	}
}
func TestQueueNativeLimitsAndUntrustedLabels(t *testing.T) {
	state := queueFixture(1000)
	long := strings.Repeat("🎵", 200) + "\n# @everyone [link](https://bad.example) **markup**"
	for i := range state.Entries {
		state.Entries[i].Track.Title = long
		state.Entries[i].Track.Artist = long
	}
	state.Current = &media.Track{Title: long, Artist: long}
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(queueSections(view)) != 1001 {
		t.Fatal("silent truncation")
	}
	for _, block := range view.Blocks {
		assertV2Budget(t, block.Container)
	}
	if strings.Contains(viewText(view), "@everyone") {
		t.Fatal("mention not neutralized")
	}
}
func TestQueueLimitValidationAndGuildIsolation(t *testing.T) {
	b, r := testBot(t)
	for _, limit := range []int{0, -1} {
		b.command(intInteraction(t, "queue", "limit", limit))
		if !strings.Contains(r.response, "at least 1") {
			t.Fatal(r.response)
		}
	}
	for _, id := range []snowflake.ID{1, 2} {
		p := b.getGuild(id).player
		if err := p.Attach(announcementVoice{}); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("Guild %d", id)
		_, err := p.BeginCollection(b.ctx, media.Collection{Tracks: []media.Track{{Title: name}}}, func(ctx context.Context, _ media.Track, _ bool) (media.Track, error) {
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		}, p.Ticket())
		if err != nil {
			t.Fatal(err)
		}
	}
	b.command(interaction(t, "queue", true))
	if len(r.update.Files) != 1 {
		t.Fatal(r.response)
	}
}
func TestQueueInternalDiagnosticsStayOutOfUI(t *testing.T) {
	state := queueFixture(1)
	state.ProviderBlocked = true
	state.Halted = true
	state.PersistenceError = "Save failed"
	state.Imports = []player.CollectionProgress{{Resolved: 2, Failed: 3}}
	state.Resolving = []player.CollectionProgress{{Total: 100, Resolved: 1}}
	view, err := buildQueueView(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := viewText(view)
	for _, word := range []string{"blocked", "halted", "Save failed", "matched", "failed", "pending", "resolving"} {
		if strings.Contains(text, word) {
			t.Fatal(text)
		}
	}
}
func TestQueueSnapshotDuringConcurrentResolution(t *testing.T) {
	b, _ := testBot(t)
	p := b.getGuild(1).player
	if err := p.Attach(announcementVoice{}); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	tracks := []media.Track{}
	for _, entry := range queueFixture(123).Entries {
		tracks = append(tracks, entry.Track)
	}
	job, err := p.BeginCollection(b.ctx, media.Collection{Tracks: tracks}, func(ctx context.Context, tr media.Track, _ bool) (media.Track, error) {
		select {
		case <-gate:
			tr.URL = "https://youtube.com/watch?v=abcdefghijk"
			return tr, nil
		case <-ctx.Done():
			return media.Track{}, ctx.Err()
		}
	}, p.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := p.State()
	close(gate)
	for range 30 {
		state := p.State()
		view, err := buildQueueView(state, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := len(state.Entries)
		if state.Current != nil {
			want++
		}
		if len(queueSections(view)) != want {
			t.Fatal("inconsistent snapshot")
		}
	}
	p.Clear()
	<-job.Done
	view, err := buildQueueView(snapshot, nil)
	if err != nil || len(queueSections(view)) != 123 {
		t.Fatal("snapshot mutated", err)
	}
}

func TestNowAndAnnouncementShareLayoutWithoutTechnicalMetadata(t *testing.T) {
	track := media.Track{Title: "War Pigs", Artist: "Black Sabbath", Duration: 475 * 1e9, SpotifyID: "secret-id", RequestedBy: "listener", SpotifyArtworkURL: "https://i.scdn.co/image/album", YouTubeThumbnailURL: "https://i.ytimg.com/other"}
	next := media.Track{Title: "Paranoid"}
	state := player.State{Current: &track, CurrentStarted: true, Entries: []player.QueueEntry{{Track: next, Pending: true}}}
	event := player.PlaybackEvent{Track: track, Next: &next, NextPending: true}
	rawNow, _ := json.Marshal(nowContainer(state))
	rawAnnouncement, _ := json.Marshal(playbackMessage(event).Components[0])
	if string(rawNow) != string(rawAnnouncement) {
		t.Fatal("inconsistent layout")
	}
	text := componentText([]d.LayoutComponent{nowContainer(state)})
	for _, forbidden := range []string{"secret-id", "listener", "Spotify metadata", "YouTube audio", "pending", "resolving", "ready", "PLAYING:"} {
		if strings.Contains(text, forbidden) {
			t.Fatal(forbidden)
		}
	}
	b, r := testBot(t)
	b.replyNow(interaction(t, "now", true), state)
	if updateText(r.update) != text {
		t.Fatal("handler did not use common layout")
	}
	state.Paused = true
	paused := componentText([]d.LayoutComponent{nowContainer(state)})
	if !strings.Contains(paused, "Paused · 07:55") {
		t.Fatal(paused)
	}
	state.CurrentStarted = false
	state.Paused = false
	if strings.Contains(componentText([]d.LayoutComponent{nowContainer(state)}), "Playing ·") {
		t.Fatal("claims playback before audio")
	}
}
