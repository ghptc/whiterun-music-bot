package media

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These adversarial summaries are deterministic: the album appears before the
// requested track, and is short enough that duration alone cannot reject it.
func TestBardTextSearchIntent(t *testing.T) {
	for _, tc := range []struct{ id, query, title string }{
		{"BARD-SEARCH-001", "Black Sabbath Paranoid", "Black Sabbath - Paranoid (Official Audio)"},
		{"BARD-SEARCH-005", "Paranoid - 2009 Remaster", "Paranoid - 2009 Remaster"},
		{"BARD-SEARCH-006", "Black Sabbath Black Sabbath", "Black Sabbath - Black Sabbath"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			data := fmt.Sprintf(`{"entries":[{"id":"album000001","title":%q,"channel":"Black Sabbath","duration":600,"categories":["Music"]},{"id":"track000001","title":%q,"channel":"Black Sabbath - Topic","duration":170,"categories":["Music"]}]}`, tc.title+" Full Album", tc.title)
			r, args := resolverFixture(t, data)
			c, progressive, err := (CollectionResolver{YouTube: r}).Prepare(context.Background(), tc.query)
			if err != nil || progressive || c.IsCollection || len(c.Tracks) != 1 || c.Tracks[0].VideoID != "track000001" {
				t.Fatalf("%+v progressive=%v err=%v", c, progressive, err)
			}
			raw, _ := os.ReadFile(args)
			if !strings.Contains(string(raw), "ytsearch10:"+tc.query) || strings.Contains(string(raw), "--yes-playlist") {
				t.Fatal(string(raw))
			}
		})
	}
}

type intentSpotify struct {
	t       *testing.T
	kind    string
	tracks  []Track
	skipped int
}

func (s intentSpotify) Collection(_ context.Context, kind, id string) (Collection, error) {
	s.t.Helper()
	if kind != s.kind || id != "1234567890123456789012" {
		s.t.Fatalf("wrong resource: %s %s", kind, id)
	}
	return Collection{Title: "Paranoid", Tracks: s.tracks, Skipped: s.skipped}, nil
}
func TestBardSpotifyResourceIntent(t *testing.T) {
	album := []Track{}
	for i, title := range []string{"War Pigs", "Paranoid", "Planet Caravan", "Iron Man", "Electric Funeral", "Hand of Doom", "Rat Salad", "Fairies Wear Boots"} {
		album = append(album, Track{Title: title, Artist: "Black Sabbath", Album: "Paranoid", SpotifyID: fmt.Sprint(i), Position: i + 1, Total: 8})
	}
	for _, tc := range []struct {
		id, kind string
		tracks   []Track
		skipped  int
	}{
		{"BARD-SEARCH-002", "track", album[1:2], 0},
		{"BARD-SEARCH-003", "album", album, 0},
		{"BARD-SEARCH-004", "playlist", append(append([]Track{}, album...), album[1]), 0},
		{"BARD-SEARCH-008", "album", []Track{album[0], album[2], album[7]}, 5},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := CollectionResolver{Spotify: intentSpotify{t, tc.kind, tc.tracks, tc.skipped}, Search: func(_ context.Context, q string) (Track, error) {
				if q != "Black Sabbath Paranoid" {
					t.Errorf("unexpected single track search: %q", q)
				}
				return Track{URL: "https://youtube.com/watch?v=track000001"}, nil
			}}
			c, pending, err := r.Prepare(context.Background(), "https://open.spotify.com/"+tc.kind+"/1234567890123456789012")
			if err != nil || pending != (tc.kind != "track") || c.IsCollection != (tc.kind != "track") || len(c.Tracks) != len(tc.tracks) || c.Skipped != tc.skipped {
				t.Fatalf("%+v %v %v", c, pending, err)
			}
			ordinary, ordinaryPending, ordinaryErr := r.PreparePlay(context.Background(), "https://open.spotify.com/"+tc.kind+"/1234567890123456789012", false)
			if ordinaryErr != nil || ordinaryPending != pending || !reflect.DeepEqual(ordinary, c) {
				t.Fatalf("force:false changed standard Spotify preparation: %+v %v %v", ordinary, ordinaryPending, ordinaryErr)
			}
			for i, tr := range c.Tracks {
				if tr.Title != tc.tracks[i].Title || tr.SpotifyID != tc.tracks[i].SpotifyID || tr.Position != tc.tracks[i].Position || tr.Total != 8 {
					t.Fatalf("position %d: %+v", i, tr)
				}
			}
		})
	}
}
func TestBardInvalidLink(t *testing.T) {
	t.Run("BARD-SEARCH-007", func(t *testing.T) {
		for _, q := range []string{"https://open.spotify.com/album/invalid", "https://example.com/album/Paranoid"} {
			_, _, err := (CollectionResolver{YouTube: Resolver{Binary: "/never-run"}}).Prepare(context.Background(), q)
			if err == nil || !strings.Contains(err.Error(), "URL") && !strings.Contains(err.Error(), "link") {
				t.Fatalf("%q: %v", q, err)
			}
		}
	})
}
func TestBardRecordingDisambiguation(t *testing.T) {
	for _, tc := range []struct{ title, artist, wrong string }{
		{"Paranoid", "Black Sabbath", "Black Sabbath - Paranoid Remastered 2009"},
		{"Paranoid - 2009 Remaster", "Black Sabbath", "Black Sabbath - Paranoid Remastered 2012"},
		{"Paranoid - Live", "Black Sabbath", "Black Sabbath - Paranoid"},
		{"Paranoid", "Black Sabbath", "Megadeth - Paranoid"},
		{"Paranoid", "Black Sabbath", "Black Sabbath - Paranoid Android"},
	} {
		t.Run(tc.wrong, func(t *testing.T) {
			tr := Track{Title: tc.title, Artist: tc.artist, Duration: 170 * time.Second}
			bad := Candidate{ID: "wrong000001", Title: tc.wrong, Channel: tc.artist + " - Topic", Duration: 170, Categories: []string{"Music"}}
			if decision := rankSpotify(tr, bad, false); decision.Reason == "accepted" {
				t.Fatalf("conflicting recording accepted: %+v", decision)
			}
			good := bad
			good.ID = "track000001"
			good.Title = tc.artist + " - " + tc.title
			got, err := selectSpotify(context.Background(), tr, []Candidate{bad, good}, func(c Candidate) (Candidate, error) { return c, nil })
			if err != nil || got.ID != good.ID {
				t.Fatal(got, err)
			}
		})
	}
}
func TestBardYouTubeMusicAlbumIntent(t *testing.T) {
	r, _ := resolverFixture(t, `{"title":"Paranoid","entries":[{"id":"track000001","title":"War Pigs"},{"id":"track000002","title":"Paranoid"}]}`)
	c, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://music.youtube.com/playlist?list=OLAK5uy_Paranoid")
	if err != nil || !c.IsCollection || len(c.Tracks) != 2 || c.Tracks[1].Title != "Paranoid" {
		t.Fatal(c, err)
	}
}

func TestBardTextNeverFallsBackToAlbum(t *testing.T) {
	r, _ := resolverFixture(t, `{"entries":[{"id":"album000001","title":"Black Sabbath - Paranoid Full Album","channel":"Black Sabbath","duration":600,"categories":["Music"]}]}`)
	c, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "Black Sabbath Paranoid")
	if err == nil || len(c.Tracks) != 0 {
		t.Fatalf("album silently accepted as track: %+v, %v", c, err)
	}
}

func TestBardAlbumUnavailableMetadataPositions(t *testing.T) {
	// BARD-SEARCH-008: exercise the real album decoder, including a missing
	// metadata entry between two available recordings.
	s := &Spotify{ClientID: "fixture", ClientSecret: "fixture", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "accounts.spotify.com":
			return jsonResponse(200, `{"access_token":"fixture","expires_in":3600}`), nil
		case strings.HasSuffix(r.URL.Path, "/tracks"):
			return jsonResponse(200, `{"total":3,"items":[{"id":"1234567890123456789012","name":"War Pigs","type":"track","artists":[{"name":"Black Sabbath"}]},null,{"id":"2234567890123456789012","name":"Planet Caravan","type":"track","artists":[{"name":"Black Sabbath"}]}],"next":null}`), nil
		default:
			return jsonResponse(200, `{"name":"Paranoid"}`), nil
		}
	})}}
	c, pending, err := (CollectionResolver{Spotify: s}).Prepare(context.Background(), "https://open.spotify.com/album/1234567890123456789012")
	if err != nil || !pending || c.Skipped != 1 || len(c.Tracks) != 2 {
		t.Fatal(c, pending, err)
	}
	for i, position := range []int{1, 3} {
		if c.Tracks[i].Position != position || c.Tracks[i].Total != 3 || c.Tracks[i].Album != "Paranoid" || c.Tracks[i].Artist != "Black Sabbath" {
			t.Fatal(c.Tracks[i])
		}
	}
}
