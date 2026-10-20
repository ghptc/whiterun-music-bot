package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
)

type hardeningConn struct {
	voice.Conn
	openErr error
	closed  bool
}

func (c *hardeningConn) Open(context.Context, snowflake.ID, bool, bool) error { return c.openErr }
func (c *hardeningConn) Close(context.Context)                                { c.closed = true }

type hardeningManager struct {
	voice.Manager
	conn voice.Conn
}

func (m hardeningManager) CreateConn(snowflake.ID) voice.Conn { return m.conn }
func TestHardeningVoiceJoinAndAttachFailures(t *testing.T) {
	for _, mode := range []string{"open failure", "attach failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			b, r := testBot(t)
			g := b.getGuild(1)
			listener(b, 1, 3, false)
			c := &hardeningConn{}
			if mode == "open failure" {
				c.openErr = errors.New("voice permission rejected")
			}
			if mode == "attach failure" {
				g.player.Close(context.Background())
			}
			b.client.VoiceManager = hardeningManager{conn: c}
			b.command(qualityInteraction(t, "summon", 3, ""))
			if mode == "success" {
				if g.conn != c || g.channel != 5 || c.closed || !strings.Contains(r.response, "answers") {
					t.Fatal("successful summon rejected", r.response)
				}
			} else {
				if g.conn != nil || !c.closed {
					t.Fatal("failed summon retained connection")
				}
				want := "permissions"
				if mode == "attach failure" {
					want = "shutting down"
				}
				if !strings.Contains(r.response, want) {
					t.Fatal("failure misreported", r.response)
				}
			}
		})
	}
}
func hardeningCommand(t *testing.T, name, options string) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	e := qualityInteraction(t, name, 3, "")
	body := fmt.Sprintf(`{"id":"10","application_id":"2","type":2,"token":"test","guild_id":"1","user":{"id":"3"},"data":{"id":"4","name":%q,"type":1,"options":%s}}`, name, options)
	if err := json.Unmarshal([]byte(body), &e.ApplicationCommandInteraction); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestHardeningAuthorizedCommandOutcomes(t *testing.T) {
	for _, tc := range []struct{ name, options, want string }{
		{"lyrics", "[]", "not playing"},
		{"previous", "[]", "No previous"},
		{"remove", `[{"name":"position","type":4,"value":1}]`, "Choose an upcoming"},
		{"clear", "[]", "already empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r := testBot(t)
			idleGuild(t, b, 1)
			listener(b, 1, 3, false)
			b.command(hardeningCommand(t, tc.name, tc.options))
			if !strings.Contains(r.response, tc.want) {
				t.Fatal("command error presented as success", r.response)
			}
		})
	}
}

type hardeningSpotify struct {
	err        error
	collection media.Collection
}

func (s hardeningSpotify) Collection(ctx context.Context, _, _ string) (media.Collection, error) {
	if err := ctx.Err(); err != nil {
		return media.Collection{}, err
	}
	return s.collection, s.err
}
func TestHardeningPlayFailureResponseAndCancellation(t *testing.T) {
	for _, mode := range []string{"Spotify authorization", "canceled search"} {
		t.Run(mode, func(t *testing.T) {
			b, r := testBot(t)
			g, _ := idleGuild(t, b, 1)
			listener(b, 1, 3, false)
			b.searches = make(chan struct{}, 4)
			b.resolver.Spotify = hardeningSpotify{err: media.ErrSpotifyAuth}
			if mode == "canceled search" {
				g.searchCancel()
			}
			b.command(qualityInteraction(t, "play", 3, "https://open.spotify.com/playlist/0123456789012345678901"))
			want := media.ErrSpotifyAuth.Error()
			if mode == "canceled search" {
				want = "Playback changed while searching"
			}
			if !strings.Contains(r.response, want) || len(b.searches) != 0 || g.player.Length() != 0 {
				t.Fatal("play failure escaped, leaked search permit or lost reason", r.response)
			}
		})
	}
}
func TestHardeningProgressiveFirstPositionNotice(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			b, r := testBot(t)
			g, _ := idleGuild(t, b, 1)
			listener(b, 1, 3, false)
			b.searches = make(chan struct{}, 4)
			tracks := []media.Track{{Title: "first", Artist: "Artist", Position: 1, Total: 1, Duration: time.Minute}}
			if skip {
				tracks = append([]media.Track{{Title: "unavailable", Artist: "Artist", Position: 1, Total: 2}}, tracks...)
				tracks[1].Position = 2
				tracks[1].Total = 2
			}
			b.resolver.Spotify = hardeningSpotify{collection: media.Collection{Tracks: tracks, IsCollection: true}}
			b.resolver.Search = func(_ context.Context, query string) (media.Track, error) {
				if strings.Contains(query, "unavailable") {
					return media.Track{}, media.ErrNoSong
				}
				return media.Track{URL: "https://youtube.com/watch?v=abcdefghijk"}, nil
			}
			b.command(qualityInteraction(t, "play", 3, "https://open.spotify.com/playlist/0123456789012345678901"))
			if strings.Contains(r.response, "remaining") || !strings.Contains(r.response, "Queued:") {
				t.Fatal("resolved final position reported as still resolving", r.response)
			}
			if strings.Contains(r.response, "earlier positions were skipped") != skip {
				t.Fatal("skipped positions misreported", r.response)
			}
			g.player.Stop()
		})
	}
}

func TestHardeningYouTubeCollectionSkippedNotice(t *testing.T) {
	b, r := testBot(t)
	idleGuild(t, b, 1)
	listener(b, 1, 3, false)
	b.searches = make(chan struct{}, 4)
	binary := filepath.Join(t.TempDir(), "yt-dlp")
	body := "#!/bin/sh\necho '{\"entries\":[{\"id\":\"abcdefghijk\",\"title\":\"valid\"},{\"id\":\"lmnopqrstuv\",\"title\":\"[Private video]\"}]}'\n"
	if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	b.resolver.YouTube.Binary = binary
	b.command(qualityInteraction(t, "play", 3, "https://www.youtube.com/playlist?list=PLfixture"))
	if !strings.Contains(r.response, "Added 1 tracks") || !strings.Contains(r.response, "1 unavailable tracks were skipped") {
		t.Fatal("partial import loss hidden", r.response)
	}
}
