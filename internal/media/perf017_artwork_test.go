package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPublicEvidenceArtworkByExactIdentity(t *testing.T) {
	body := `<script id="initialState">` + base64.StdEncoding.EncodeToString([]byte(`{"entities":{"items":{"spotify:playlist:fixture":{"content":{"totalCount":1,"pagingInfo":{"nextOffset":null},"items":[{"itemV2":{"data":{"uri":"spotify:track:26rdOwwjC2UnweK3xeS58u","albumOfTrack":{"coverArt":{"sources":[{"url":"https://i.scdn.co/image/gdfr"}]}}}}}]}}}}}`)) + `</script>`
	e, err := spotifyPlaylistEvidence([]byte(body), "fixture")
	if err != nil || e.Artwork["26rdOwwjC2UnweK3xeS58u"] != "https://i.scdn.co/image/gdfr" {
		t.Fatal(e, err)
	}
	if e.Artwork["different"] != "" {
		t.Fatal("artwork crossed identity")
	}
}

func TestTrackEmbedVisualIdentityArtwork(t *testing.T) {
	body := []byte(`<script id="__NEXT_DATA__">{"props":{"pageProps":{"state":{"data":{"entity":{"type":"track","uri":"spotify:track:26rdOwwjC2UnweK3xeS58u","title":"GDFR","artists":[{"name":"Flo Rida"}],"visualIdentity":{"image":[{"url":"https://image-cdn-ak.spotifycdn.com/image/gdfr"}]}}}}}}}</script>`)
	track, err := parseSpotifyTrackEmbed(body, "spotify:track:26rdOwwjC2UnweK3xeS58u")
	if err != nil || ArtworkURL(track) != "https://image-cdn-ak.spotifycdn.com/image/gdfr" {
		t.Fatal(track, err)
	}
}

func TestPlaylistFallbackRestoresPublicArtwork(t *testing.T) {
	d, e := embedFixture(t)
	evidence := evidencePage(embedEvidence(e), 30)
	encoded := scriptData([]byte(evidence), "initialState")
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	content := envelope["entities"].(map[string]any)["items"].(map[string]any)["spotify:playlist:"+publicID].(map[string]any)["content"].(map[string]any)
	items := content["items"].([]any)
	first := items[0].(map[string]any)["itemV2"].(map[string]any)["data"].(map[string]any)
	first["albumOfTrack"] = map[string]any{"coverArt": map[string]any{"sources": []any{map[string]any{"url": "https://i.scdn.co/image/exact-album"}}}}
	raw, _ = json.Marshal(envelope)
	evidence = strings.Replace(evidence, encoded, base64.StdEncoding.EncodeToString(raw), 1)
	calls := 0
	s := &Spotify{Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasPrefix(r.URL.Path, "/embed/playlist/") {
			return jsonResponse(200, embedHTML(d)), nil
		}
		return jsonResponse(200, evidence), nil
	})}}
	c, err := s.publicPlaylist(context.Background(), publicID)
	if err != nil || len(c.Tracks) != 50 || calls != 2 {
		t.Fatal(len(c.Tracks), calls, err)
	}
	if c.Tracks[0].SpotifyArtworkURL != "https://i.scdn.co/image/exact-album" || c.Tracks[1].SpotifyArtworkURL != "" {
		t.Fatal("missing or crossed artwork", c.Tracks[:2])
	}
}
