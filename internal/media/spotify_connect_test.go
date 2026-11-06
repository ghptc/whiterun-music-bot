package media

import (
	"context"
	"testing"
	"time"
)

func TestNativeSpotifyExactIdentityAndOrder(t *testing.T) {
	a := Track{SpotifyID: "0123456789ABCDEFGHIJKL", Title: "Radio Edit", Artist: "Artist", Duration: time.Minute, RequestedSource: "spotify"}
	b := a
	b.SpotifyID = "1123456789ABCDEFGHIJKL"
	b.Title = "Extended Remix"
	r := CollectionResolver{NativeSpotify: true, Spotify: fakeSpotify{Collection{Tracks: []Track{a, b, a}, IsCollection: true}}, YouTube: Resolver{Binary: "/must-not-run"}}
	c, progressive, err := r.Prepare(context.Background(), "https://open.spotify.com/playlist/0123456789ABCDEFGHIJKL")
	if err != nil || progressive || len(c.Tracks) != 3 {
		t.Fatal(c, progressive, err)
	}
	for i, id := range []string{a.SpotifyID, b.SpotifyID, a.SpotifyID} {
		if c.Tracks[i].URL != "spotify:track:"+id || c.Tracks[i].RequestedSource != "spotify_connect" {
			t.Fatal(c.Tracks[i])
		}
	}
}
func TestNativeSpotifyRejectsRelinking(t *testing.T) {
	a := Track{SpotifyID: "0123456789ABCDEFGHIJKL", Duration: time.Minute, SpotifyOriginalID: "1123456789ABCDEFGHIJKL"}
	if _, err := nativeSpotifyTrack(a); err == nil {
		t.Fatal("silent relinking")
	}
}
