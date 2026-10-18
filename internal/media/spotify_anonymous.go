package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const spotifyPublicPageSize = 50

// A session belongs to one resolution only: never persisted, shared or logged.
// Exactly one bootstrap refresh is allowed, including expiry and HTTP 401.
type spotifyAnonymousSession struct {
	spotify   *Spotify
	id        string
	token     string
	expires   time.Time
	refreshed bool
}

func (a *spotifyAnonymousSession) bootstrap(body []byte) error {
	var e spotifyEmbedEnvelope
	if json.Unmarshal([]byte(scriptData(body, "__NEXT_DATA__")), &e) != nil {
		return fmt.Errorf("%w: invalid anonymous bootstrap", ErrSpotifyPageParse)
	}
	state := e.Props.PageProps.State
	session := state.Settings.Session
	if state.Data.Entity.URI != "spotify:playlist:"+a.id || !session.Anonymous || session.Token == "" || len(session.Token) > 16384 || session.Expires <= 0 {
		return fmt.Errorf("%w: missing anonymous session", ErrSpotifyPageParse)
	}
	a.token = session.Token
	a.expires = time.UnixMilli(session.Expires)
	return nil
}
func (a *spotifyAnonymousSession) refresh(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			a.spotify.stage(ctx, "spotify_anonymous_refresh_failed", "reason", spotifyErrorCategory(err))
		}
	}()
	if a.refreshed {
		return fmt.Errorf("%w: anonymous session expired or rejected", ErrSpotifyPageFetch)
	}
	a.refreshed = true
	a.spotify.stage(ctx, "spotify_anonymous_refresh_start", "retry", 1)
	body, err := a.spotify.spotifyPublicPage(ctx, "embed/playlist/"+a.id)
	if err != nil {
		return err
	}
	if err = a.bootstrap(body); err != nil {
		return err
	}
	if !time.Now().Add(10 * time.Second).Before(a.expires) {
		return fmt.Errorf("%w: anonymous session expired", ErrSpotifyPageFetch)
	}
	a.spotify.stage(ctx, "spotify_anonymous_refresh_complete", "expires_in_seconds", int(time.Until(a.expires).Seconds()))
	return nil
}

type spotifyPublicItems struct {
	Revision string `json:"revision"`
	Total    *int   `json:"length"`
	Contents struct {
		Position  *int  `json:"pos"`
		Truncated *bool `json:"truncated"`
		Items     []struct {
			URI        string `json:"uri"`
			Attributes struct {
				ID string `json:"itemId"`
			} `json:"attributes"`
		} `json:"items"`
	} `json:"contents"`
}

func (a *spotifyAnonymousSession) page(ctx context.Context, offset int) (page spotifyPublicItems, err error) {
	defer func() {
		if err != nil {
			a.spotify.stage(ctx, "spotify_spclient_page_failed", "offset", offset, "reason", spotifyErrorCategory(err))
		}
	}()
	a.spotify.stage(ctx, "spotify_spclient_page_start", "offset", offset, "page_size", spotifyPublicPageSize)
	for attempt := 0; attempt < 2; attempt++ {
		if !time.Now().Add(10 * time.Second).Before(a.expires) {
			if err := a.refresh(ctx); err != nil {
				return page, err
			}
		}
		body, status, err := a.request(ctx, offset)
		a.spotify.stage(ctx, "spotify_spclient_response", "offset", offset, "status", status, "retry", attempt, "reason", spotifyErrorCategory(err))
		if err != nil {
			return page, err
		}
		if status == http.StatusUnauthorized && !a.refreshed {
			if err := a.refresh(ctx); err != nil {
				return page, err
			}
			continue
		}
		if status != http.StatusOK {
			return page, fmt.Errorf("%w: anonymous metadata status=%d", ErrSpotifyPageFetch, status)
		}
		if json.Unmarshal(body, &page) != nil {
			return page, fmt.Errorf("%w: malformed playlist page", ErrSpotifyPageParse)
		}
		a.spotify.stage(ctx, "spotify_spclient_page_complete", "offset", offset, "entries", len(page.Contents.Items))
		return page, nil
	}
	return page, ErrSpotifyPageFetch
}
func (a *spotifyAnonymousSession) request(ctx context.Context, offset int) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Construct all URLs locally. Never follow server-supplied URLs or redirects.
	target := fmt.Sprintf("https://spclient.wg.spotify.com/playlist/v2/playlist/%s?from=%d&length=%d", a.id, offset, spotifyPublicPageSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, ErrSpotifyPageFetch
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; BardMetadata/1.0)")
	client := *a.spotify.client()
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		a.spotify.stage(ctx, "spotify_http_failed", "host", "spclient.wg.spotify.com", "endpoint", "playlist_metadata", "offset", offset, "reason", spotifyErrorCategory(err))
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, safeSpotifyFetchFailure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSpotifyPageBytes+1))
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if err != nil || len(body) > maxSpotifyPageBytes {
		reason := "body_read"
		if len(body) > maxSpotifyPageBytes {
			reason = "body_limit"
		}
		a.spotify.stage(ctx, "spotify_http_failed", "host", "spclient.wg.spotify.com", "endpoint", "playlist_metadata", "offset", offset, "reason", reason)
		return nil, 0, &spotifyFetchFailure{reason: reason}
	}
	return body, resp.StatusCode, nil
}

func validateSpotifyPublicItems(p spotifyPublicItems, offset, total int, revision string) error {
	c := p.Contents
	if p.Total == nil || *p.Total != total || p.Revision == "" || (revision != "" && p.Revision != revision) || c.Position == nil || *c.Position != offset || c.Truncated == nil {
		return fmt.Errorf("%w: playlist count, revision or offset changed", ErrSpotifyIncomplete)
	}
	expected := min(spotifyPublicPageSize, total-offset)
	if len(c.Items) != expected || expected <= 0 || *c.Truncated != (offset+len(c.Items) < total) {
		return fmt.Errorf("%w: empty, incomplete or non-advancing page", ErrSpotifyIncomplete)
	}
	return nil
}

// anonymousPlaylist combines ordered occurrence pagination with public Embed
// metadata. Only fully hydrated, revision-consistent collections escape.
func (s *Spotify) anonymousPlaylist(ctx context.Context, id string, embed []byte, evidence playlistEvidence) (result Collection, err error) {
	stage := "spotify_anonymous_validation"
	defer func() {
		if err != nil {
			s.stage(ctx, stage+"_failed", "reason", spotifyErrorCategory(err))
		}
	}()
	if !spotifyID.MatchString(id) || evidence.Total <= 0 || evidence.Total > MaxCollectionTracks {
		return Collection{}, ErrSpotifyIncomplete
	}
	var envelope spotifyEmbedEnvelope
	if json.Unmarshal([]byte(scriptData(embed, "__NEXT_DATA__")), &envelope) != nil {
		return Collection{}, ErrSpotifyPageParse
	}
	n := len(envelope.Props.PageProps.State.Data.Entity.Tracks)
	if n >= evidence.Total || n < len(evidence.Prefix) {
		return Collection{}, ErrSpotifyIncomplete
	}
	// Validate every cached entry, identity, occurrence UID and the public prefix.
	cached, err := parseSpotifyEmbed(embed, id, playlistEvidence{Total: n, Prefix: evidence.Prefix})
	if err != nil {
		return Collection{}, err
	}
	stage = "spotify_anonymous_bootstrap"
	s.stage(ctx, "spotify_anonymous_bootstrap_start")
	session := spotifyAnonymousSession{spotify: s, id: id}
	if err = session.bootstrap(embed); err != nil {
		return Collection{}, err
	}
	s.stage(ctx, "spotify_anonymous_bootstrap_complete", "expires_in_seconds", int(time.Until(session.expires).Seconds()))
	stage = "spotify_spclient_pagination"
	uris := make([]string, 0, evidence.Total)
	seen := make(map[string]bool)
	revision := ""
	for offset := 0; offset < evidence.Total; offset += spotifyPublicPageSize {
		if err = ctx.Err(); err != nil {
			return Collection{}, err
		}
		page, e := session.page(ctx, offset)
		if e != nil {
			return Collection{}, e
		}
		if e = validateSpotifyPublicItems(page, offset, evidence.Total, revision); e != nil {
			s.stage(ctx, "spotify_spclient_validation_failed", "offset", offset, "expected", min(spotifyPublicPageSize, evidence.Total-offset), "actual", len(page.Contents.Items), "reason", "count_revision_or_offset_mismatch")
			return Collection{}, e
		}
		revision = page.Revision
		for _, item := range page.Contents.Items {
			if !strings.HasPrefix(item.URI, "spotify:track:") || !spotifyID.MatchString(strings.TrimPrefix(item.URI, "spotify:track:")) || item.Attributes.ID == "" || seen[item.Attributes.ID] {
				return Collection{}, fmt.Errorf("%w: invalid or repeated playlist occurrence", ErrSpotifyIncomplete)
			}
			seen[item.Attributes.ID] = true
			uris = append(uris, item.URI)
		}
	}
	stage = "spotify_anonymous_prefix_validation"
	metadata := make(map[string]Track)
	for i, track := range cached.Tracks {
		uri := "spotify:track:" + strings.TrimPrefix(track.SourceURL, "https://open.spotify.com/track/")
		if uris[i] != uri {
			return Collection{}, fmt.Errorf("%w: Embed/pagination order mismatch", ErrSpotifyIncomplete)
		}
		metadata[uri] = track
	}
	// Deduplicate metadata requests, never playlist occurrences.
	missing := make([]string, 0)
	for _, uri := range uris {
		if _, ok := metadata[uri]; !ok {
			metadata[uri] = Track{}
			missing = append(missing, uri)
		}
	}
	stage = "spotify_tail_enrichment"
	s.stage(ctx, "spotify_tail_enrichment_start", "missing", len(missing))
	hydrated, err := s.hydrateSpotifyTracks(ctx, missing)
	if err != nil {
		return Collection{}, err
	}
	s.stage(ctx, "spotify_tail_enrichment_complete", "resolved", len(hydrated))
	for i, track := range hydrated {
		metadata[missing[i]] = track
	}
	// An edit during hydration invalidates the entire batch, even at equal length.
	stage = "spotify_revision_validation"
	last, err := session.page(ctx, 0)
	if err != nil {
		return Collection{}, err
	}
	if err = validateSpotifyPublicItems(last, 0, evidence.Total, revision); err != nil {
		return Collection{}, err
	}
	for i, item := range last.Contents.Items {
		if item.URI != uris[i] {
			return Collection{}, ErrSpotifyIncomplete
		}
	}
	result = Collection{Title: cached.Title, SourceURL: cached.SourceURL, IsCollection: true}
	for _, uri := range uris {
		result.Tracks = append(result.Tracks, metadata[uri])
	}
	if ctx.Err() != nil {
		return Collection{}, ctx.Err()
	}
	return result, nil
}
func (s *Spotify) hydrateSpotifyTracks(ctx context.Context, uris []string) ([]Track, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tracks := make([]Track, len(uris))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for range min(4, len(uris)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				body, err := s.spotifyPublicPage(ctx, "embed/track/"+strings.TrimPrefix(uris[i], "spotify:track:"))
				if err == nil {
					tracks[i], err = parseSpotifyTrackEmbed(body, uris[i])
				}
				if err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
dispatch:
	for i := range uris {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return tracks, nil
}
func parseSpotifyTrackEmbed(body []byte, uri string) (Track, error) {
	var envelope spotifyEmbedEnvelope
	if json.Unmarshal([]byte(scriptData(body, "__NEXT_DATA__")), &envelope) != nil {
		return Track{}, ErrSpotifyPageParse
	}
	e := envelope.Props.PageProps.State.Data.Entity
	if e.Type != "track" || e.URI != uri || strings.TrimSpace(e.Title) == "" || e.Duration < 0 {
		return Track{}, ErrSpotifyPageParse
	}
	for _, artist := range e.Artists {
		if strings.TrimSpace(artist.Name) == "" {
			return Track{}, ErrSpotifyPageParse
		}
	}
	t, ok := spotifyMetadata(&spotifyTrack{ID: strings.TrimPrefix(uri, "spotify:track:"), Type: e.Type, Name: e.Title, DurationMS: e.Duration, Artists: e.Artists})
	if !ok {
		return Track{}, ErrSpotifyPageParse
	}
	return t, nil
}
