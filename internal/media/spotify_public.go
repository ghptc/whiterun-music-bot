package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var (
	ErrSpotifyPageFetch  = errors.New("Could not fetch Spotify public playlist metadata.")
	ErrSpotifyPageParse  = errors.New("Could not parse Spotify public playlist metadata.")
	ErrSpotifyIncomplete = errors.New("Spotify public page does not expose a verifiably complete playlist; nothing was queued.")
)

const maxSpotifyPageBytes = 8 << 20

// publicPlaylist fetches anonymous metadata only. It never forwards API credentials.
func (s *Spotify) publicPlaylist(ctx context.Context, id string) (result Collection, err error) {
	stage := "spotify_public_page"
	defer func() {
		if err != nil {
			s.stage(ctx, stage+"_failed", "reason", spotifyErrorCategory(err))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	body, err := s.spotifyPublicPage(ctx, "playlist/"+id)
	if err != nil {
		return Collection{}, err
	}
	result, err = parseSpotifyPlaylist(body, id)
	if errors.Is(err, ErrSpotifyIncomplete) {
		// Embed has no independent total; require evidence from the public page.
		evidence, evidenceErr := spotifyPlaylistEvidence(body, id)
		if evidenceErr == nil {
			s.stage(ctx, "spotify_public_page_complete", "rendered", len(evidence.Prefix), "total", evidence.Total)
			stage = "spotify_embed"
			embed, fetchErr := s.spotifyPublicPage(ctx, "embed/playlist/"+id)
			if fetchErr != nil {
				return Collection{}, errors.Join(err, fetchErr)
			}
			var envelope spotifyEmbedEnvelope
			if json.Unmarshal([]byte(scriptData(embed, "__NEXT_DATA__")), &envelope) == nil {
				s.stage(ctx, "spotify_embed_complete", "embedded", len(envelope.Props.PageProps.State.Data.Entity.Tracks), "total", evidence.Total)
			}
			result, evidenceErr = parseSpotifyEmbed(embed, id, evidence)
			if errors.Is(evidenceErr, ErrSpotifyIncomplete) {
				stage = "spotify_anonymous"
				result, evidenceErr = s.anonymousPlaylist(ctx, id, embed, evidence)
			}
			if evidenceErr != nil {
				return Collection{}, errors.Join(err, evidenceErr)
			}
			err = nil
		}
	}
	if ctx.Err() != nil {
		return Collection{}, ctx.Err()
	}
	if err == nil {
		s.stage(ctx, "spotify_public_complete", "resolved", len(result.Tracks), "total", len(result.Tracks))
	}
	return result, err
}

func (s *Spotify) spotifyPublicPage(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	category := spotifyPublicEndpoint(path)
	reason := "request_setup"
	status := 0
	defer func() {
		if reason != "" {
			s.stage(ctx, "spotify_http_failed", "endpoint", category, "host", "open.spotify.com", "status", status, "reason", reason)
		}
	}()
	target := "https://open.spotify.com/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSpotifyPageFetch
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; BardMetadata/1.0)")
	req.Header.Set("Accept", "text/html")
	client := *s.client()
	// The fallback is anonymous even if the configured client has a cookie jar.
	client.Jar = nil
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.Host != "open.spotify.com" || req.URL.User != nil {
			return errors.New("unsupported Spotify public page redirect")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		reason = spotifyErrorCategory(err)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, safeSpotifyFetchFailure(err)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	reason = "http_status"
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status=%d", ErrSpotifyPageFetch, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSpotifyPageBytes+1))
	reason = "body_read"
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSpotifyPageFetch
	}
	if len(body) > maxSpotifyPageBytes {
		reason = "body_limit"
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrSpotifyPageFetch, maxSpotifyPageBytes)
	}
	if err := ctx.Err(); err != nil {
		reason = spotifyErrorCategory(err)
		return nil, err
	}
	reason = ""
	return body, nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func spotifyLinkID(href, kind string) string {
	u, err := url.Parse(href)
	if err != nil || u.User != nil || (u.Host != "" && u.Host != "open.spotify.com") || (u.Scheme != "" && u.Scheme != "https") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != kind || !spotifyID.MatchString(parts[1]) {
		return ""
	}
	return parts[1]
}

// The public initialState is used only as completeness evidence, scoped to
// this playlist. Track metadata comes from human-visible semantic HTML rows.
// Unknown/changed state schemas fail closed rather than silently truncating.
func parseSpotifyPlaylist(body []byte, id string) (Collection, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return Collection{}, fmt.Errorf("%w: %w", ErrSpotifyPageParse, err)
	}
	result := Collection{SourceURL: "https://open.spotify.com/playlist/" + id, IsCollection: true}
	var rows []*html.Node
	var state string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if attr(n, "data-testid") == "track-row" {
			rows = append(rows, n)
			return
		}
		if n.Data == "script" && attr(n, "id") == "initialState" {
			state = nodeText(n)
		}
		if n.Data == "meta" && attr(n, "property") == "og:title" {
			result.Title = attr(n, "content")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(rows) > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	for i, row := range rows {
		track := spotifyTrack{Type: "track"}
		var visit func(*html.Node)
		visit = func(n *html.Node) {
			if n.Data == "a" {
				if tid := spotifyLinkID(attr(n, "href"), "track"); tid != "" {
					if track.ID != "" && track.ID != tid {
						track.Type = "ambiguous"
					}
					track.ID = tid
					if title := nodeText(n); title != "" {
						track.Name = title
					}
				}
				if spotifyLinkID(attr(n, "href"), "artist") != "" {
					name := nodeText(n)
					if name != "" {
						track.Artists = append(track.Artists, struct {
							Name string `json:"name"`
						}{name})
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				visit(c)
			}
		}
		visit(row)
		if track.Name == "" {
			track.Name = attr(row, "aria-label")
		}
		metadata, ok := spotifyMetadata(&track)
		if !ok {
			return Collection{}, fmt.Errorf("%w: malformed track row %d", ErrSpotifyPageParse, i+1)
		}
		// Repeated IDs can be intentional playlist entries; preserve occurrences.
		result.Tracks = append(result.Tracks, metadata)
	}
	var envelope struct {
		Entities struct {
			Items map[string]struct {
				Name    string `json:"name"`
				Content struct {
					Total  *int              `json:"totalCount"`
					Items  []json.RawMessage `json:"items"`
					Paging *struct {
						Next *int `json:"nextOffset"`
					} `json:"pagingInfo"`
				} `json:"content"`
			} `json:"items"`
		} `json:"entities"`
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(state)
	if decodeErr != nil || json.Unmarshal(decoded, &envelope) != nil {
		return Collection{}, fmt.Errorf("%w: missing or invalid playlist count", ErrSpotifyIncomplete)
	}
	entity, ok := envelope.Entities.Items["spotify:playlist:"+id]
	if !ok || entity.Content.Total == nil || *entity.Content.Total < 0 || entity.Content.Paging == nil {
		return Collection{}, fmt.Errorf("%w: missing playlist count/paging", ErrSpotifyIncomplete)
	}
	total := *entity.Content.Total
	if total > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	if total != len(rows) || len(entity.Content.Items) != total || entity.Content.Paging.Next != nil {
		return Collection{}, fmt.Errorf("%w: rendered=%d total=%d", ErrSpotifyIncomplete, len(rows), total)
	}
	if result.Title == "" {
		result.Title = entity.Name
	}
	if total == 0 {
		return Collection{}, ErrNoTracks
	}
	return result, nil
}

func spotifyPublicEndpoint(path string) string {
	switch {
	case strings.HasPrefix(path, "embed/playlist/"):
		return "embed_playlist"
	case strings.HasPrefix(path, "embed/track/"):
		return "embed_track"
	case strings.HasPrefix(path, "playlist/"):
		return "public_playlist"
	default:
		return "public_page"
	}
}
