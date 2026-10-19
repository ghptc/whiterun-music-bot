package discord

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	d "github.com/disgoorg/disgo/discord"
	"whiterun/internal/media"
)

func pngBytes(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := range 10 {
		for x := range 10 {
			img.Set(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func fakeArtworkRenderer(body []byte, calls *int) *queueImageRenderer {
	r := newQueueImageRenderer()
	r.client = &http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	return r
}
func TestQueuePNGBlocksAndPixelOrder(t *testing.T) {
	for _, n := range []int{0, 1, 10, 123, 500} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			view, err := buildQueueView(queueFixture(n), nil)
			if err != nil {
				t.Fatal(err)
			}
			renderer := newQueueImageRenderer()
			next := 1
			for i := range view.Blocks {
				block := &view.Blocks[i]
				if n == 0 {
					if len(block.Rows) != 0 {
						t.Fatal("nonempty")
					}
					continue
				}
				for _, row := range block.Rows {
					if row.Position != next {
						t.Fatal("order")
					}
					next++
				}
				if err := renderer.render(context.Background(), block); err != nil {
					t.Fatal(err)
				}
				img, err := png.Decode(bytes.NewReader(block.PNG))
				if err != nil {
					t.Fatal(err)
				}
				if img.Bounds().Dx() != queueWidth || img.Bounds().Dy() != 88+queueRowHeight*len(block.Rows) || len(block.PNG) > queuePNGMax {
					t.Fatal("size", img.Bounds(), len(block.PNG))
				}
				if len(block.update("").Files) != 1 || len(block.Container.Components) != 2 {
					t.Fatal("attachment budget")
				}
				assertV2Budget(t, block.Container)
				if len(queueAlt(block.Rows)) > 1024 {
					t.Fatal("alt budget")
				}
			}
			if next != n+1 {
				t.Fatal(next)
			}
		})
	}
	// Distinct covers must stay on the left and in snapshot order.
	r := newQueueImageRenderer()
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	for key, c := range map[string]color.Color{"red": red, "blue": blue} {
		img := image.NewRGBA(image.Rect(0, 0, 48, 48))
		for y := range 48 {
			for x := range 48 {
				img.Set(x, y, c)
			}
		}
		r.cache["https://i.scdn.co/"+key] = cachedQueueArt{img, time.Now().Add(time.Hour)}
	}
	block := queueBlock{Rows: []queueRow{{Position: 1, Track: media.Track{Title: "First", SpotifyArtworkURL: "https://i.scdn.co/red"}}, {Position: 2, Track: media.Track{Title: "Second", SpotifyArtworkURL: "https://i.scdn.co/blue"}}}, Container: musicContainer("QUEUE", d.TextDisplayComponent{Content: "footer"})}
	if err := r.render(context.Background(), &block); err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(block.PNG))
	if img.At(80, 80) != red || img.At(80, 80+queueRowHeight) != blue {
		t.Fatal("artwork moved/reordered")
	}
}
func TestQueueArtworkCacheFailuresAndLimits(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"valid", pngBytes(t, color.White), true}, {"invalid", []byte("not an image"), false}, {"large body", bytes.Repeat([]byte("x"), artworkBytesMax+1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			r := fakeArtworkRenderer(test.body, &calls)
			for range 2 {
				img := r.artwork(context.Background(), "https://i.scdn.co/test")
				if (img != nil) != test.valid {
					t.Fatal("decode")
				}
			}
			if calls != 1 {
				t.Fatal("positive/negative cache miss", calls)
			}
			r.cache["https://i.scdn.co/test"] = cachedQueueArt{expires: time.Now().Add(-time.Second)}
			r.artwork(context.Background(), "https://i.scdn.co/test")
			if calls != 2 {
				t.Fatal("expired cache")
			}
			for i := range artworkCacheMax - len(r.cache) {
				r.cache[fmt.Sprintf("https://i.scdn.co/%d", i)] = cachedQueueArt{expires: time.Now().Add(time.Hour)}
			}
			r.artwork(context.Background(), "https://i.scdn.co/new")
			if len(r.cache) > artworkCacheMax {
				t.Fatal("cache unbounded")
			}
			for _, url := range []string{"http://i.scdn.co/a", "https://localhost/a", "https://i.scdn.co.evil/a", "file:///tmp/a", "https://i.scdn.co:443/a"} {
				before := calls
				if r.artwork(context.Background(), url) != nil || calls != before {
					t.Fatal("unsafe host", url)
				}
			}
		})
	}
	// DecodeConfig rejects oversized images before allocating the pixel buffer.
	giant := image.NewGray(image.Rect(0, 0, 2001, 2000))
	var buf bytes.Buffer
	_ = png.Encode(&buf, giant)
	calls := 0
	r := fakeArtworkRenderer(buf.Bytes(), &calls)
	if r.artwork(context.Background(), "https://i.scdn.co/giant") != nil {
		t.Fatal("pixel cap")
	}
}
func TestQueuePNGLongUnicodeAndRenderFailures(t *testing.T) {
	state := queueFixture(10)
	for i := range state.Entries {
		state.Entries[i].Track = media.Track{Title: strings.Repeat("日本語 🎵 João Привет Ελληνικά\n", 200), Artist: "Björk"}
	}
	view, _ := buildQueueView(state, nil)
	r := newQueueImageRenderer()
	if err := r.render(context.Background(), &view.Blocks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(view.Blocks[0].PNG)); err != nil {
		t.Fatal(err)
	}
	if err := r.render(context.Background(), &queueBlock{}); err == nil {
		t.Fatal("empty image accepted")
	}
	if err := r.render(context.Background(), &queueBlock{Rows: make([]queueRow, 13)}); err == nil {
		t.Fatal("row cap")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.render(ctx, &view.Blocks[0]); err != context.Canceled {
		t.Fatal(err)
	}
	var capped cappedPNG
	if _, err := capped.Write(make([]byte, queuePNGMax+1)); err == nil || capped.Len() != 0 {
		t.Fatal("byte cap")
	}
}
func TestQueueArtworkYouTubeFallback(t *testing.T) {
	r := newQueueImageRenderer()
	var hosts []string
	valid := pngBytes(t, color.White)
	r.client = &http.Client{Transport: queueTransport(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		data := []byte("bad")
		if req.URL.Host == "i.ytimg.com" {
			data = valid
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	state := queueFixture(1)
	state.Entries[0].Track.SpotifyArtworkURL = "https://i.scdn.co/bad"
	state.Entries[0].Track.YouTubeThumbnailURL = "https://i.ytimg.com/good"
	view, _ := buildQueueView(state, nil)
	if err := r.render(context.Background(), &view.Blocks[0]); err != nil {
		t.Fatal(err)
	}
	if strings.Join(hosts, ",") != "i.scdn.co,i.ytimg.com" {
		t.Fatal(hosts)
	}
}
func TestQueueConcurrentRenderAndCache(t *testing.T) {
	r := newQueueImageRenderer()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			view, _ := buildQueueView(queueFixture(10), nil)
			if err := r.render(context.Background(), &view.Blocks[0]); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
