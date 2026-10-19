package discord

import (
	"context"
	"fmt"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"strings"
	"testing"
	"unicode/utf16"

	d "github.com/disgoorg/disgo/discord"
	"whiterun/internal/media"
	"whiterun/internal/player"
)

func queueAttachment(t *testing.T, m d.MessageUpdate, name string) string {
	t.Helper()
	for _, f := range m.Files {
		if f.Name == name {
			// Production uses fresh readers per delivery; tests restore after inspecting.
			b, err := io.ReadAll(f.Reader)
			if err != nil {
				t.Fatal(err)
			}
			f.Reader = strings.NewReader(string(b))
			return string(b)
		}
	}
	t.Fatalf("missing %s", name)
	return ""
}
func queueFixture(n int) player.State {
	s := player.State{}
	for i := range n {
		s.Entries = append(s.Entries, player.QueueEntry{Track: media.Track{Title: fmt.Sprintf("Song %04d", i+1), Artist: "Artist", SpotifyID: fmt.Sprint(i), SpotifyArtworkURL: "https://i.scdn.co/image/album", RequestedBy: "listener"}, Pending: i%2 == 0})
	}
	return s
}
func TestQueueCompleteAndLimitedSnapshots(t *testing.T) {
	for _, size := range []int{0, 1, 10, 123, 500, 1000} {
		for _, limit := range []*int{nil, new(1), new(10), new(50), new(2000)} {
			t.Run(fmt.Sprintf("%d/%v", size, limit), func(t *testing.T) {
				s := queueFixture(size)
				v, err := buildQueueView(s, limit)
				if err != nil {
					t.Fatal(err)
				}
				want := size
				if limit != nil {
					want = min(want, *limit)
				}
				if v.Shown != want || v.Total != size {
					t.Fatal(v.Shown, v.Total)
				}
				prev := -1
				for i := range size {
					label := fmt.Sprintf("Song %04d", i+1)
					at := strings.Index(v.HTML, label)
					if (at >= 0) != (i < want) {
						t.Fatal("omitted or extra position", i)
					}
					if i < want {
						if at <= prev || !strings.Contains(v.Text, label) {
							t.Fatal("order or text omission", i)
						}
						prev = at
					}
				}
				if len(v.Embeds) != min(want, 10) {
					t.Fatal(len(v.Embeds))
				}
				if size == 0 && (!strings.Contains(v.Content, "queue is empty") || !strings.Contains(v.Content, "nothing playing")) {
					t.Fatal(v.Content)
				}
				if limit != nil && !strings.Contains(v.Content, fmt.Sprintf("Showing %d of %d", want, size)) {
					t.Fatal(v.Content)
				}
			})
		}
	}
}
func TestQueueCurrentPendingAndArtwork(t *testing.T) {
	s := queueFixture(1)
	s.Current = &media.Track{Title: "Current", Artist: "Current artist", YouTubeThumbnailURL: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}
	s.Paused = true
	v, err := buildQueueView(s, new(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Embeds) != 2 || v.Embeds[0].Color == v.Embeds[1].Color || !strings.Contains(v.Embeds[0].Title, "Current") || !strings.Contains(v.Embeds[0].Description, "paused") {
		t.Fatal(v.Embeds)
	}
	if v.Embeds[0].Thumbnail.URL != s.Current.YouTubeThumbnailURL || v.Embeds[1].Thumbnail.URL != s.Entries[0].Track.SpotifyArtworkURL || !strings.Contains(v.HTML, "resolving") || !strings.Contains(v.Text, "listener") {
		t.Fatal(v)
	}
	v, err = buildQueueView(player.State{Current: s.Current}, nil)
	if err != nil || len(v.Embeds) != 1 || !strings.Contains(v.Content, "1 total including current") {
		t.Fatal(v, err)
	}
	v, err = buildQueueView(player.State{Entries: []player.QueueEntry{{Track: media.Track{Title: "No image"}}}}, nil)
	if err != nil || v.Embeds[0].Thumbnail != nil || !strings.Contains(v.HTML, "No artwork") {
		t.Fatal(v, err)
	}
}
func TestQueueNativeLimitsAndSafeGallery(t *testing.T) {
	s := queueFixture(1000)
	long := strings.Repeat("🎵", 200) + ` <script>alert(1)</script> @everyone **markup**`
	s.Current = &media.Track{Title: long, Artist: long, RequestedBy: long}
	s.Resolving = []player.CollectionProgress{{Total: 1000, Resolved: 1}}
	for i := range s.Entries {
		s.Entries[i].Track.Title = long
		s.Entries[i].Track.Artist = long
		s.Entries[i].Track.RequestedBy = long
	}
	v, err := buildQueueView(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(utf16.Encode([]rune(v.Content))) > 2000 || len(v.Embeds) > 10 {
		t.Fatal("message size")
	}
	units := 0
	for _, embed := range v.Embeds {
		units += len(utf16.Encode([]rune(embed.Title + embed.Description)))
		if len(utf16.Encode([]rune(embed.Title))) > 256 {
			t.Fatal(embed)
		}
	}
	if units > 6000 {
		t.Fatal("embed budget", units)
	}
	if len(v.HTML) > 8<<20 || len(v.Text) > 8<<20 || strings.Contains(v.HTML, "<script>") || !strings.Contains(v.HTML, "&lt;script&gt;") || !strings.Contains(v.Text, long) {
		t.Fatal("export size/escaping/completeness")
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
		if err := p.Attach(transport{conn: &dummyConn{}}); err != nil {
			t.Fatal(err)
		}
		name := "Guild one"
		if id == 2 {
			name = "Guild two"
		}
		if _, err := p.BeginCollection(b.ctx, media.Collection{Tracks: []media.Track{{Title: name}}}, func(ctx context.Context, _ media.Track, _ bool) (media.Track, error) {
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		}, p.Ticket()); err != nil {
			t.Fatal(err)
		}
	}

	b.command(interaction(t, "queue", true))
	text := queueAttachment(t, r.update, "queue.txt")
	if !strings.Contains(text, "Guild one") || strings.Contains(text, "Guild two") {
		t.Fatal(text)
	}
	for _, limit := range []int{1, 10, 50} {
		b.command(intInteraction(t, "queue", "limit", limit))
		if !strings.Contains(r.response, "Showing 1 of 1") {
			t.Fatal(r.response)
		}
	}
}
func TestQueueBlockedAndImportState(t *testing.T) {
	s := queueFixture(1)
	s.ProviderBlocked = true
	s.Halted = true
	s.PersistenceError = "Save failed."
	s.Imports = []player.CollectionProgress{{Resolved: 2, Failed: 3, Uncertain: 1, MetadataSkipped: 1, Skipped: 1}}
	v, err := buildQueueView(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"Playback blocked", "use /resume", "Playback halted", "Save failed", "2 matched", "1 uncertain", "2 rejected", "1 unavailable", "1 skipped"} {
		if !strings.Contains(v.Content, part) {
			t.Fatal(v.Content)
		}
	}
}
func TestQueueSnapshotDuringConcurrentResolution(t *testing.T) {
	b, _ := testBot(t)
	p := b.getGuild(1).player
	if err := p.Attach(transport{conn: &dummyConn{}}); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	rows := queueFixture(123)
	tracks := []media.Track{}
	for _, e := range rows.Entries {
		tracks = append(tracks, e.Track)
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
		v, err := buildQueueView(state, nil)
		if err != nil {
			t.Fatal(err)
		}
		expected := len(state.Entries)
		if state.Current != nil {
			expected++
		}
		if strings.Count(v.HTML, "<li") != expected {
			t.Fatal("inconsistent snapshot")
		}
	}
	// An earlier snapshot remains detached even after matching/clear.
	p.Clear()
	<-job.Done
	v, err := buildQueueView(snapshot, nil)
	if err != nil || strings.Count(v.HTML, "<li") != 123 || !strings.Contains(v.HTML, "resolving") {
		t.Fatal("snapshot mutated", err)
	}
}
