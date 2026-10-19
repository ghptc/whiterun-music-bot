package discord

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	d "github.com/disgoorg/disgo/discord"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"whiterun/internal/media"
)

const queueWidth = 960
const queueRowHeight = 64
const queuePNGMax = 4 << 20
const artworkBytesMax = 2 << 20
const artworkPixelsMax = 4_000_000
const artworkCacheMax = 256

type queueRow struct {
	Track    media.Track
	Position int
}
type cachedQueueArt struct {
	image   image.Image
	expires time.Time
}
type queueImageRenderer struct {
	warmSlots chan struct{}
	slots     chan struct{}
	mu        sync.Mutex
	cache     map[string]cachedQueueArt
	client    *http.Client
}

func newQueueImageRenderer() *queueImageRenderer {
	return &queueImageRenderer{warmSlots: make(chan struct{}, 2), slots: make(chan struct{}, 2), cache: make(map[string]cachedQueueArt), client: &http.Client{Timeout: 800 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var queueImages = newQueueImageRenderer()

// Provider hosts only: artwork metadata cannot turn the bot into an arbitrary
// URL fetcher. Redirects, oversized bodies and decompression bombs are rejected.
func allowedQueueArtwork(raw string) bool {
	u, err := url.Parse(media.SafeArtworkURL(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Host == "i.scdn.co" || u.Host == "i.ytimg.com" || u.Host == "img.youtube.com"
}
func (r *queueImageRenderer) artwork(ctx context.Context, raw string) image.Image {
	if !allowedQueueArtwork(raw) {
		return nil
	}
	r.mu.Lock()
	hit, ok := r.cache[raw]
	r.mu.Unlock()
	if ok && time.Now().Before(hit.expires) {
		return hit.image
	}
	if ctx.Err() != nil {
		return nil
	}
	var thumb image.Image
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err == nil {
		resp, e := r.client.Do(req)
		if e == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				data, e := io.ReadAll(io.LimitReader(resp.Body, artworkBytesMax+1))
				if e == nil && len(data) <= artworkBytesMax {
					cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
					if e == nil && cfg.Width > 0 && cfg.Height > 0 && int64(cfg.Width)*int64(cfg.Height) <= artworkPixelsMax {
						src, _, e := image.Decode(bytes.NewReader(data))
						if e == nil {
							small := image.NewRGBA(image.Rect(0, 0, 48, 48))
							bounds := src.Bounds()
							side := min(bounds.Dx(), bounds.Dy())
							center := bounds.Min.Add(image.Pt(bounds.Dx()/2, bounds.Dy()/2))
							crop := image.Rect(center.X-side/2, center.Y-side/2, center.X-side/2+side, center.Y-side/2+side)
							xdraw.ApproxBiLinear.Scale(small, small.Bounds(), src, crop, draw.Src, nil)
							thumb = small
						}
					}
				}
			}
		}
	}
	ttl := time.Hour
	if thumb == nil {
		ttl = 5 * time.Minute
	}
	r.mu.Lock()
	if len(r.cache) >= artworkCacheMax {
		for key := range r.cache {
			delete(r.cache, key)
			break
		}
	}
	r.cache[raw] = cachedQueueArt{thumb, time.Now().Add(ttl)}
	r.mu.Unlock()
	return thumb
}

// Warm artwork separately; admission is nonblocking and bounds goroutines,
// HTTP concurrency and decoded image memory even across many guilds.
func (r *queueImageRenderer) warm(ctx context.Context, rows []queueRow) {
	select {
	case r.warmSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-r.warmSlots }()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		for _, row := range rows {
			if ctx.Err() != nil {
				return
			}
			r.artwork(ctx, media.ArtworkURL(row.Track))
		}
	}()
}

func (r *queueImageRenderer) cachedArtwork(raw string) image.Image {
	r.mu.Lock()
	defer r.mu.Unlock()
	hit := r.cache[raw]
	if time.Now().Before(hit.expires) {
		return hit.image
	}
	return nil
}

func imageLabel(s string) string { return queueClip(strings.Join(strings.Fields(s), " "), 512) }
func drawQueueText(dst draw.Image, face font.Face, x, y, width int, text string, ink color.Color) {
	text = imageLabel(text)
	if font.MeasureString(face, text).Ceil() > width {
		runes := []rune(text)
		for len(runes) > 0 && font.MeasureString(face, string(runes)+"…").Ceil() > width {
			runes = runes[:len(runes)-1]
		}
		text = string(runes) + "…"
	}
	drawer := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face, Dot: fixed.P(x, y)}
	drawer.DrawString(text)
}

type cappedPNG struct{ bytes.Buffer }

func (b *cappedPNG) Write(p []byte) (int, error) {
	if b.Len()+len(p) > queuePNGMax {
		return 0, fmt.Errorf("queue PNG exceeds byte limit")
	}
	return b.Buffer.Write(p)
}

func (r *queueImageRenderer) render(ctx context.Context, block *queueBlock) error {
	if len(block.Rows) == 0 || len(block.Rows) > queueBlockSize {
		return fmt.Errorf("invalid queue image row count")
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return err
	}
	title, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 22, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return err
	}
	defer title.Close()
	subtitle, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 17, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return err
	}
	defer subtitle.Close()
	bg := color.RGBA{18, 18, 18, 255}
	white := color.RGBA{240, 240, 240, 255}
	gray := color.RGBA{166, 166, 166, 255}
	green := color.RGBA{30, 215, 96, 255}
	dst := image.NewRGBA(image.Rect(0, 0, queueWidth, 88+queueRowHeight*len(block.Rows)))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	drawQueueText(dst, title, 24, 34, 900, "THE BARD · QUEUE", white)
	drawQueueText(dst, subtitle, 24, 63, 900, block.rangeLabel(), gray)
	for i, row := range block.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		y := 80 + i*queueRowHeight
		position := fmt.Sprint(row.Position)
		ink := color.Color(gray)
		if row.Position == 0 {
			position = "▶"
			ink = green
		}
		drawQueueText(dst, subtitle, 24, y+31, 48, position, ink)
		art := r.cachedArtwork(media.ArtworkURL(row.Track))
		if art == nil && row.Track.SpotifyArtworkURL != "" {
			fallback := row.Track
			fallback.SpotifyArtworkURL = ""
			art = r.cachedArtwork(media.ArtworkURL(fallback))
		}
		rect := image.Rect(80, y, 128, y+48)
		if art != nil {
			draw.Draw(dst, rect, art, art.Bounds().Min, draw.Src)
		} else {
			draw.Draw(dst, rect, image.NewUniform(color.RGBA{42, 42, 42, 255}), image.Point{}, draw.Src)
			drawQueueText(dst, title, 93, y+32, 30, "♪", gray)
		}
		name := row.Track.Title
		if strings.TrimSpace(name) == "" {
			name = "Untitled"
		}
		artist := row.Track.Artist
		if artist == "" {
			artist = row.Track.Channel
		}
		drawQueueText(dst, title, 148, y+22, queueWidth-172, name, white)
		drawQueueText(dst, subtitle, 148, y+45, queueWidth-172, artist, gray)
	}
	var encoded cappedPNG
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&encoded, dst); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	block.PNG = encoded.Bytes()
	footer := block.Container.Components[len(block.Container.Components)-1]
	block.Container = d.ContainerComponent{Components: []d.ContainerSubComponent{
		d.MediaGalleryComponent{Items: []d.MediaGalleryItem{{Media: d.UnfurledMediaItem{URL: "attachment://queue.png"}, Description: queueAlt(block.Rows)}}}, footer,
	}}
	return nil
}

// Keep image alternative text below Discord's 1024-character budget.
func queueAlt(rows []queueRow) string {
	var lines []string
	for _, row := range rows {
		prefix := fmt.Sprintf("%d. ", row.Position)
		if row.Position == 0 {
			prefix = "Now playing: "
		}
		artist := row.Track.Artist
		if artist == "" {
			artist = row.Track.Channel
		}
		lines = append(lines, prefix+musicLabel(row.Track.Title, 40)+" · "+musicLabel(artist, 24))
	}
	return strings.Join(lines, "\n")
}
