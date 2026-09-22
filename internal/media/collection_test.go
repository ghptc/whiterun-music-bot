package media

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestYouTubeCollectionPartialAndLazy(t *testing.T) {
	r, args := resolverFixture(t, `{"title":"Album","entries":[{"id":"abcdefghijk","title":"First"},null,{"id":"12345678901","title":"[Private video]"},{"id":"lmnopqrstuv","title":"Second"}]}`)
	// Nonzero exit with usable metadata must retain valid entries.
	f, err := os.OpenFile(r.Binary, os.O_APPEND|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("exit 1\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk&list=PLtest")
	if err != nil || len(got.Tracks) != 2 || got.Skipped != 2 || got.Title != "Album" || got.Tracks[0].Title != "First" || got.Tracks[1].Title != "Second" || !got.Tracks[0].VerifyBeforePlay {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := os.ReadFile(args)
	for _, want := range []string{"--yes-playlist", "--flat-playlist", "--playlist-end\n501", "https://www.youtube.com/playlist?list=PLtest"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s: %s", want, b)
		}
	}
}
func TestCollectionEmptyAndLimit(t *testing.T) {
	for _, tt := range []struct {
		json string
		want error
	}{{`{"entries":[null]}`, ErrNoTracks}, {`{"playlist_count":501,"entries":[]}`, ErrCollectionTooLarge}} {
		r, _ := resolverFixture(t, tt.json)
		_, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://youtube.com/playlist?list=PLtest")
		if !errors.Is(err, tt.want) {
			t.Fatalf("%v", err)
		}
	}
}

type fakeSpotify struct{ result Collection }

func (s fakeSpotify) Collection(context.Context, string, string) (Collection, error) {
	return s.result, nil
}
func TestSpotifyMatchingPartialOrderAndMetadata(t *testing.T) {
	first := make(chan struct{})
	r := CollectionResolver{Spotify: fakeSpotify{Collection{Skipped: 1, Tracks: []Track{{Title: "First", Artist: "Artist", SourceURL: "original"}, {Title: "Missing", Artist: "Artist"}, {Title: "Last", Artist: "Artist"}}}}, Search: func(ctx context.Context, q string) (Track, error) {
		switch q {
		case "Artist First":
			<-first
		case "Artist Missing":
			return Track{}, ErrNoSong
		case "Artist Last":
			close(first)
		default:
			t.Errorf("query %q", q)
		}
		return Track{URL: "https://youtube.com/watch?v=abcdefghijk", Title: "YouTube title"}, nil
	}}
	got, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/1234567890123456789012")
	if err != nil || len(got.Tracks) != 2 || got.Skipped != 2 || got.Tracks[0].Title != "First" || got.Tracks[1].Title != "Last" || got.Tracks[0].SourceURL != "original" || got.Tracks[0].Artist != "Artist" || got.Tracks[0].RequestedSource != "spotify" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestCollectionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := CollectionResolver{Spotify: fakeSpotify{Collection{Tracks: []Track{{Title: "song"}}}}, Search: func(ctx context.Context, _ string) (Track, error) { return Track{}, ctx.Err() }}
	_, err := r.Resolve(ctx, "https://open.spotify.com/album/1234567890123456789012")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestCollectionURL(t *testing.T) {
	for _, tt := range []struct {
		input, source, kind string
		bad                 bool
	}{
		{"https://music.youtube.com/playlist?list=OLAK5uy_test", "youtube", "playlist", false},
		{"https://youtu.be/abcdefghijk?list=PL123", "youtube", "playlist", false},
		{"https://open.spotify.com/intl-pt/album/1234567890123456789012?si=x", "spotify", "album", false},
		{"https://evil.test/playlist?list=PL123", "", "", false},
		{"https://youtube.com:443/playlist?list=PL123", "", "", true},
		{"https://open.spotify.com/playlist/nope", "", "", true},
		{"The Strokes Someday", "", "", false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			source, kind, _, err := CollectionURL(tt.input)
			if source != tt.source || kind != tt.kind || (err != nil) != tt.bad {
				t.Fatalf("%s %s %v", source, kind, err)
			}
		})
	}
}

func TestReportedSpotifyPlaylistURL(t *testing.T) {
	const query = "https://open.spotify.com/playlist/37i9dQZF1DZ06evO1sJmec?si=1f277a033f9b4f28"
	source, kind, id, err := CollectionURL(query)
	if err != nil || source != "spotify" || kind != "playlist" || id != "37i9dQZF1DZ06evO1sJmec" {
		t.Fatalf("source=%q kind=%q id=%q error=%v", source, kind, id, err)
	}
	// With Spotify unconfigured this must fail at Spotify configuration, never
	// enter the single-video resolver (whose binary deliberately does not exist).
	r := CollectionResolver{YouTube: Resolver{Binary: "/nonexistent/yt-dlp"}}
	if _, err := r.Resolve(context.Background(), query); !errors.Is(err, ErrSpotifyConfig) {
		t.Fatalf("incorrect resolver selected: %v", err)
	}
}
