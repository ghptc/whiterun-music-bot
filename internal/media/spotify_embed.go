package media

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

type playlistEvidence struct {
	Total  int
	Prefix []string
}

// scriptData preserves JSON string whitespace (notably Embed's artist delimiter).
// Ambiguous bootstrap scripts fail closed. No bootstrap/session data is logged.
func scriptData(body []byte, id string) string {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	var data strings.Builder
	count := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && attr(n, "id") == id {
			count++
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					data.WriteString(c.Data)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if count != 1 {
		return ""
	}
	return data.String()
}

func spotifyPlaylistEvidence(body []byte, id string) (playlistEvidence, error) {
	var envelope struct {
		Entities struct {
			Items map[string]struct {
				Content struct {
					Total *int `json:"totalCount"`
					Items []struct {
						Item struct {
							Data struct {
								URI string `json:"uri"`
							} `json:"data"`
						} `json:"itemV2"`
					} `json:"items"`
					Paging *struct {
						Next *int `json:"nextOffset"`
					} `json:"pagingInfo"`
				} `json:"content"`
			} `json:"items"`
		} `json:"entities"`
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(scriptData(body, "initialState")))
	if err != nil || json.Unmarshal(data, &envelope) != nil {
		return playlistEvidence{}, ErrSpotifyIncomplete
	}
	content := envelope.Entities.Items["spotify:playlist:"+id].Content
	if content.Total == nil || *content.Total <= 0 || content.Paging == nil || len(content.Items) == 0 || len(content.Items) > *content.Total {
		return playlistEvidence{}, ErrSpotifyIncomplete
	}
	if *content.Total > MaxCollectionTracks {
		return playlistEvidence{}, ErrCollectionTooLarge
	}
	// The initial page must actually be a prefix, with an advancing next offset.
	if len(content.Items) < *content.Total {
		if content.Paging.Next == nil || *content.Paging.Next != len(content.Items) {
			return playlistEvidence{}, ErrSpotifyIncomplete
		}
	} else if content.Paging.Next != nil {
		return playlistEvidence{}, ErrSpotifyIncomplete
	}
	evidence := playlistEvidence{Total: *content.Total}
	for _, item := range content.Items {
		uri := item.Item.Data.URI
		if !strings.HasPrefix(uri, "spotify:track:") || !spotifyID.MatchString(strings.TrimPrefix(uri, "spotify:track:")) {
			return playlistEvidence{}, ErrSpotifyIncomplete
		}
		evidence.Prefix = append(evidence.Prefix, uri)
	}
	return evidence, nil
}

// Only metadata and anonymous-session fields are decoded; audio fields are ignored.
type spotifyEmbedEnvelope struct {
	Props struct {
		PageProps struct {
			State struct {
				Data struct {
					Entity spotifyEmbedEntity `json:"entity"`
				} `json:"data"`
				Settings struct {
					Session struct {
						Token     string `json:"accessToken"`
						Expires   int64  `json:"accessTokenExpirationTimestampMs"`
						Anonymous bool   `json:"isAnonymous"`
					} `json:"session"`
				} `json:"settings"`
			} `json:"state"`
		} `json:"pageProps"`
	} `json:"props"`
}
type spotifyEmbedEntity struct {
	Type     string `json:"type"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Duration int    `json:"duration"`
	Artists  []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Tracks []struct {
		URI      string `json:"uri"`
		UID      string `json:"uid"`
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Type     string `json:"entityType"`
		Duration int    `json:"duration"`
	} `json:"trackList"`
}

// Embed is a single snapshot, not a pagination API. Only accept its ordered
// entries when the independent public total and entire exposed prefix agree.
func parseSpotifyEmbed(body []byte, id string, evidence playlistEvidence) (Collection, error) {
	var envelope spotifyEmbedEnvelope
	if json.Unmarshal([]byte(scriptData(body, "__NEXT_DATA__")), &envelope) != nil {
		return Collection{}, fmt.Errorf("%w: invalid Embed bootstrap", ErrSpotifyPageParse)
	}
	entity := envelope.Props.PageProps.State.Data.Entity
	if entity.Type != "playlist" || entity.URI != "spotify:playlist:"+id {
		return Collection{}, fmt.Errorf("%w: Embed playlist identity mismatch", ErrSpotifyPageParse)
	}
	if evidence.Total > MaxCollectionTracks || len(entity.Tracks) > MaxCollectionTracks {
		return Collection{}, ErrCollectionTooLarge
	}
	if evidence.Total <= 0 || len(evidence.Prefix) == 0 || len(evidence.Prefix) > evidence.Total || len(entity.Tracks) != evidence.Total {
		return Collection{}, fmt.Errorf("%w: embedded=%d total=%d", ErrSpotifyIncomplete, len(entity.Tracks), evidence.Total)
	}
	result := Collection{Title: entity.Name, SourceURL: "https://open.spotify.com/playlist/" + id, IsCollection: true}
	seen := make(map[string]bool)
	for i, entry := range entity.Tracks {
		if i < len(evidence.Prefix) && entry.URI != evidence.Prefix[i] {
			return Collection{}, fmt.Errorf("%w: Embed/public order mismatch at entry %d", ErrSpotifyIncomplete, i+1)
		}
		// UIDs identify playlist occurrences, not tracks. Repeated track IDs are valid.
		if entry.UID == "" || seen[entry.UID] {
			return Collection{}, fmt.Errorf("%w: missing or repeated Embed entry UID at %d", ErrSpotifyIncomplete, i+1)
		}
		seen[entry.UID] = true
		if !strings.HasPrefix(entry.URI, "spotify:track:") || strings.TrimSpace(entry.Title) == "" || entry.Duration < 0 {
			return Collection{}, fmt.Errorf("%w: malformed Embed entry %d", ErrSpotifyPageParse, i+1)
		}
		track := spotifyTrack{ID: strings.TrimPrefix(entry.URI, "spotify:track:"), Type: entry.Type, Name: entry.Title, DurationMS: entry.Duration}
		// Spotify separates artist credits with comma + NBSP, not ordinary commas
		// (which may be part of an artist's name). Do not normalize before splitting.
		for _, artist := range strings.Split(entry.Subtitle, ",\u00a0") {
			artist = strings.TrimSpace(artist)
			if artist == "" {
				return Collection{}, fmt.Errorf("%w: missing Embed artist at %d", ErrSpotifyPageParse, i+1)
			}
			track.Artists = append(track.Artists, struct {
				Name string `json:"name"`
			}{artist})
		}
		metadata, ok := spotifyMetadata(&track)
		if !ok {
			return Collection{}, fmt.Errorf("%w: malformed Embed entry %d", ErrSpotifyPageParse, i+1)
		}
		result.Tracks = append(result.Tracks, metadata)
	}
	return result, nil
}
