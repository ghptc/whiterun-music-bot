package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"html/template"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

func TestCommandSyncUpsertsAndReadsBackWithoutDeleting(t *testing.T) {
	for _, scope := range []snowflake.ID{0, 1} {
		for _, fail := range []string{"", "publish", "readback", "mismatch"} {
			t.Run(fmt.Sprint(scope, "/", fail), func(t *testing.T) {
				b, _ := testBot(t)
				published := []json.RawMessage{json.RawMessage(`{"id":"99","application_id":"2","type":1,"name":"unrelated","description":"Retain me"}`)}
				client := rest.NewClient("fixture", rest.WithLogger(b.log), rest.WithHTTPClient(&http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
					status := 200
					body := ""
					switch req.Method {
					case "POST":
						raw, _ := io.ReadAll(req.Body)
						var payload map[string]any
						if err := json.Unmarshal(raw, &payload); err != nil {
							t.Fatal(err)
						}
						if payload["name"] == "queue" {
							options := payload["options"].([]any)
							if len(options) != 1 || options[0].(map[string]any)["name"] != "limit" || options[0].(map[string]any)["type"] != float64(4) {
								t.Fatal(payload)
							}
						}
						payload["id"] = "4"
						payload["application_id"] = "2"
						payload["version"] = "5"
						raw, _ = json.Marshal(payload)
						published = append(published, raw)
						body = string(raw)
						if fail == "publish" {
							status = 403
							body = `{"message":"denied","code":50013}`
						}
					case "GET":
						raw, _ := json.Marshal(published)
						body = string(raw)
						if fail == "readback" {
							status = 403
							body = `{"message":"denied","code":50013}`
						}
						if fail == "mismatch" {
							body = "[]"
						}
					default:
						t.Fatalf("destructive command request: %s", req.Method)
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})}))
				b.client.Rest = rest.New(client)
				err := b.syncCommands(context.Background(), scope)
				if (err != nil) != (fail != "") {
					t.Fatal(err)
				}
				if fail == "" {
					count := len(published)
					if err := b.syncCommands(context.Background(), scope); err != nil {
						t.Fatal(err)
					}
					if len(published) != count || count != len(commands())+1 {
						t.Fatal("unchanged definitions republished or unrelated command lost")
					}
				}
			})
		}
	}
}

func TestNowPlayingEmbedArtworkAndNativeLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		track media.Track
		art   string
	}{
		{"spotify", media.Track{SpotifyArtworkURL: "https://i.scdn.co/image/a", YouTubeThumbnailURL: "https://i.ytimg.com/a"}, "https://i.scdn.co/image/a"},
		{"spotify fallback", media.Track{RequestedSource: "spotify", VideoID: "abcdefghijk"}, "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"},
		{"youtube", media.Track{YouTubeThumbnailURL: "https://i.ytimg.com/a"}, "https://i.ytimg.com/a"},
		{"invalid", media.Track{SpotifyArtworkURL: "javascript:alert(1)"}, ""},
		{"none", media.Track{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.track.Title = strings.Repeat("🎵", 400)
			tc.track.Artist = "Artist"
			tc.track.RequestedBy = "listener"
			tc.track.Position = 2
			tc.track.Total = 10
			m := playbackMessage(player.PlaybackEvent{Track: tc.track, Next: &media.Track{Title: "Next track"}})
			if len(m.Embeds) != 1 || m.Content != "" || m.AllowedMentions == nil {
				t.Fatal(m)
			}
			e := m.Embeds[0]
			if queueClip(e.Title, 256) != e.Title || !strings.Contains(e.Description, "Artist") || !strings.Contains(e.Description, "2/10") || !strings.Contains(e.Description, "Next track") {
				t.Fatal(e)
			}
			if tc.art == "" {
				if e.Thumbnail != nil {
					t.Fatal(e)
				}
			} else if e.Thumbnail == nil || e.Thumbnail.URL != tc.art {
				t.Fatal(e)
			}
		})
	}
}

func TestSupersededStartAnnouncementIsDropped(t *testing.T) {
	b, r := testBot(t)
	g := b.getGuild(1)
	g.announcementChannel.Store(9)
	g.announcements <- announcement{channel: 9, epoch: g.announcementEpoch.Load(), event: player.PlaybackEvent{Sequence: 99, Track: media.Track{Title: "never started"}}, queued: time.Now()}
	// A failure event acts as a barrier through the same worker.
	g.announcements <- announcement{channel: 9, epoch: g.announcementEpoch.Load(), event: player.PlaybackEvent{SkippedReason: "barrier"}, queued: time.Now()}
	select {
	case m := <-r.messages:
		if strings.Contains(m.message.Content, "Now Playing") || len(m.message.Embeds) > 0 {
			t.Fatal(m)
		}
	case <-time.After(time.Second):
		t.Fatal("worker blocked")
	}
}

func BenchmarkQueue500(b *testing.B) {
	state := queueFixture(500)
	for b.Loop() {
		v, err := buildQueueView(state, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(v.HTML)), "html-bytes")
		b.ReportMetric(float64(len(v.Text)), "txt-bytes")
	}
}

func TestSimultaneousGuildEmbedsUseFinalPlaybackMetadata(t *testing.T) {
	b, r := testBot(t)
	start := make(chan struct{})
	for _, id := range []snowflake.ID{1, 2} {
		g := b.getGuild(id)
		g.player.Close(context.Background())
		g.player = player.New(b.ctx, b.log, func(ctx context.Context, track media.Track, v player.Voice) error {
			<-start
			track.Title = "started " + track.Title
			track.YouTubeThumbnailURL = "https://i.ytimg.com/vi/" + track.VideoID + "/hqdefault.jpg"
			v.(interface{ SetPlaybackTrack(media.Track) }).SetPlaybackTrack(track)
			if err := v.WriteFrame(ctx, []byte{1}); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
		b.bindAnnouncements(g)
		g.announcementChannel.Store(uint64(id + 10))
		if err := g.player.Attach(announcementVoice{}); err != nil {
			t.Fatal(err)
		}
		if err := g.player.EnqueueMany([]media.Track{{Title: fmt.Sprint(id), VideoID: "abcdefghijk"}}, g.player.Ticket()); err != nil {
			t.Fatal(err)
		}
	}
	close(start)
	seen := map[snowflake.ID]bool{}
	for range 2 {
		select {
		case m := <-r.messages:
			if seen[m.channel] || len(m.message.Embeds) != 1 {
				t.Fatal(m)
			}
			e := m.message.Embeds[0]
			if !strings.Contains(e.Title, fmt.Sprint("started ", m.channel-10)) || e.Thumbnail == nil {
				t.Fatal(e)
			}
			seen[m.channel] = true
		case <-time.After(time.Second):
			t.Fatal("guild announcement missing")
		}
	}
}

func TestQueueHTMLFailureRetainsCompleteText(t *testing.T) {
	original := queueGallery
	queueGallery = template.Must(template.New("broken").Parse("{{.MissingField}}"))
	defer func() { queueGallery = original }()
	v, err := buildQueueView(queueFixture(500), nil)
	if err == nil || !strings.Contains(v.Text, "Song 0500") {
		t.Fatal(v.Text, err)
	}
	b, r := testBot(t)
	b.replyQueue(interaction(t, "queue", true), queueFixture(500), nil)
	if len(r.update.Files) != 1 || r.update.Files[0].Name != "queue.txt" || !strings.Contains(queueAttachment(t, r.update, "queue.txt"), "Song 0500") {
		t.Fatal(r.update)
	}
}

type queueProviderTrap struct{ t *testing.T }

func (p queueProviderTrap) Collection(context.Context, string, string) (media.Collection, error) {
	p.t.Fatal("external Spotify call from queue")
	return media.Collection{}, nil
}
func TestQueueDoesNotResolvePendingMedia(t *testing.T) {
	b, r := testBot(t)
	b.resolver = media.CollectionResolver{Spotify: queueProviderTrap{t}, Search: func(context.Context, string) (media.Track, error) {
		t.Fatal("media search from queue")
		return media.Track{}, nil
	}, YouTube: media.Resolver{Binary: "/nonexistent/cr002"}}
	db, err := storage.Open(t.TempDir() + "/queue.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tracks := []media.Track{{Title: "pending", RequestedSource: "spotify", SpotifyArtworkURL: "https://i.scdn.co/image/pending"}}
	if err = db.SaveGuild(context.Background(), "1", player.DurableState{Queue: tracks}); err != nil {
		t.Fatal(err)
	}
	b.ConfigureStore(db)
	g := b.getGuild(1)
	b.command(interaction(t, "queue", true))
	if len(*r.update.Embeds) != 1 || (*r.update.Embeds)[0].Thumbnail == nil || g.player.State().Entries[0].Track.URL != "" {
		t.Fatal(r.update)
	}
}
