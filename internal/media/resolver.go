package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrNoSong = errors.New("The Bard could find no song worthy of playing.")

type Track struct {
	ForcedYouTube       bool `json:",omitempty"`
	identityProof       *Candidate
	SpotifyArtworkURL   string `json:",omitempty"`
	YouTubeThumbnailURL string `json:",omitempty"`
	SpotifyID           string
	ISRC                string
	Explicit            *bool
	RequestedBy         string
	MatchStatus         MatchStatus
	MatchReason         string
	Live                bool
	Position, Total     int
	RequestedSource     string
	SourceURL           string
	Artist              string
	Artists             []string
	Album               string
	VerifyBeforePlay    bool
	VideoID             string
	Channel             string
	Title               string
	URL                 string
	Duration            time.Duration
	Timing              *Timing `json:"-"`
}
type Candidate struct {
	Thumbnail    string    `json:"thumbnail"`
	CheckedAt    time.Time `json:"bard_checked_at,omitempty"`
	ISRC         string    `json:"isrc"`
	Explicit     *bool     `json:"explicit"`
	Type         string    `json:"_type"`
	Artists      []string  `json:"artists"`
	Album        string    `json:"album"`
	MusicSearch  bool      `json:"-"`
	ChannelID    string    `json:"channel_id"`
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Channel      string    `json:"channel"`
	Uploader     string    `json:"uploader"`
	Description  string    `json:"description"`
	Categories   []string  `json:"categories"`
	Duration     float64   `json:"duration"`
	Artist       string    `json:"artist"`
	Track        string    `json:"track"`
	Verified     bool      `json:"channel_is_verified"`
	Availability string    `json:"availability"`
	Live         bool      `json:"is_live"`
	LiveStatus   string    `json:"live_status"`
}

type Resolver struct {
	Binary string
	Cache  *SearchCache
	// Match is immutable structured metadata for a Spotify-originated search.
	Match    *Track
	Policy   *MatchPolicy
	Mappings MappingStore
}

func (r Resolver) Resolve(ctx context.Context, query string) (Track, error) {
	if err := r.matchPolicy().Validate(); err != nil {
		return Track{}, err
	}
	started := time.Now()
	timing := TimingFrom(ctx)
	timing.Event("resolver_start", started)
	defer func() { timing.Event("resolver_complete", started) }()
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 500 {
		return Track{}, errors.New("Give the Bard a song name or YouTube video URL (up to 500 characters).")
	}
	direct, isURL, err := YouTubeURL(query)
	if err != nil {
		return Track{}, err
	}
	if err := ctx.Err(); err != nil {
		return Track{}, err
	}
	key := normalizeQuery(query)
	if isURL {
		key = direct
	}
	if r.Match != nil {
		key = "spotify:" + MappingKey(*r.Match, r.Policy)
	}
	if track, ok := r.Cache.get(key, time.Now()); ok {
		track.Timing = timing
		timing.Event("resolver_cache_hit", started, "selected", track.Title)
		return track, nil
	}
	work := func(workCtx context.Context) (Track, error) { return r.resolveUncached(workCtx, query, direct, isURL) }
	track, err := r.Cache.resolve(ctx, key, work)
	if err != nil {
		return Track{}, err
	}
	track.Timing = timing
	return track, nil
}

type searchResult struct {
	Candidate
	Entries []*Candidate `json:"entries"`
}

func (r Resolver) extract(ctx context.Context, target, purpose string, flat bool) (searchResult, error) {
	args := MetadataArgs()
	count := 1
	if flat {
		args = append(BaseArgs(), "--flat-playlist", "--playlist-end", "10")
		count = 10
	}
	args = append(args, "--skip-download", "--dump-single-json", "--ignore-errors", "--", target)
	cmd := Command(ctx, r.Binary, args...)
	out := &LimitedBuffer{Limit: 8 << 20}
	stderr := &LimitedBuffer{Limit: 8192}
	cmd.Stdout, cmd.Stderr = out, stderr
	started := time.Now()
	timing := TimingFrom(ctx)
	timing.Event("yt_dlp_start", started, "purpose", purpose, "category", "metadata", "flat", flat, "candidates", count, "target", target, "started_at", started.UTC())
	runErr := cmd.Run()
	timing.Event("yt_dlp_complete", started, "purpose", purpose, "flat", flat, "candidates", count, "error", runErr, "cancel_reason", context.Cause(ctx), "context_error", ctx.Err())
	if runErr != nil {
		return searchResult{}, fmt.Errorf("YouTube resolution failed: %w: %s", errors.Join(runErr, ctx.Err()), SafeDiagnostic(stderr.Data))
	}
	if out.Truncated {
		return searchResult{}, errors.New("YouTube metadata exceeded the size limit")
	}
	parseStart := time.Now()
	var result searchResult
	if err := json.Unmarshal(out.Data, &result); err != nil {
		return result, fmt.Errorf("invalid YouTube metadata: %w", err)
	}
	if !validDurationSeconds(result.Duration) {
		return searchResult{}, errors.New("invalid YouTube duration")
	}
	for i, candidate := range result.Entries {
		if candidate != nil && !validDurationSeconds(candidate.Duration) {
			result.Entries[i] = nil
		}
	}
	// Provider JSON cannot set the trusted validation timestamp.
	result.CheckedAt = time.Time{}
	if !flat {
		result.CheckedAt = time.Now()
	}
	timing.Event("candidate_parsing_complete", parseStart, "entries", len(result.Entries), "purpose", purpose)
	return result, nil
}

func (r Resolver) resolveUncached(ctx context.Context, query, direct string, isURL bool) (Track, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	started := time.Now()
	timing := TimingFrom(ctx)
	var chosen Candidate
	if isURL {
		result, err := r.extract(ctx, direct, "direct_video_metadata", false)
		if err != nil {
			return Track{}, err
		}
		chosen = result.Candidate
		if "https://www.youtube.com/watch?v="+chosen.ID != direct || chosen.LiveStatus == "is_upcoming" {
			return Track{}, ErrNoSong
		}
	} else if r.Match != nil {
		var err error
		chosen, err = r.resolveSpotify(ctx, query)
		if err != nil {
			return Track{}, err
		}
	} else {
		result, err := r.extract(ctx, "ytsearch10:"+query, "flat_search", true)
		if err != nil {
			return Track{}, err
		}
		candidates := make([]Candidate, 0, len(result.Entries))
		for _, c := range result.Entries {
			if c != nil {
				candidates = append(candidates, *c)
			}
		}
		// A general search can omit the canonical release entirely. One
		// bounded audio-oriented discovery pass broadens the candidate pool;
		// rank it against the ORIGINAL request so version intent is unchanged.
		canonical := false
		variant := has(query, "video")
		for _, v := range variants {
			variant = variant || requested(query, v)
		}
		for _, c := range candidates {
			breakdown, relevant := musicScoreBreakdown(query, c)
			state, _ := ClassifyDiscovery(query, c)
			if state != Rejected && relevant && breakdown.FinalScore >= 400 && breakdown.VersionScore >= 0 && sourceBonus(query, c) >= 60 && matchesMetadata(r.Match, c) {
				canonical = true
			}
		}
		if !canonical && !variant {
			extra, extraErr := r.extract(ctx, "ytsearch10:"+query+" official audio", "canonical_audio_search", true)
			if ctx.Err() != nil {
				return Track{}, ctx.Err()
			}
			if extraErr == nil {
				seen := make(map[string]bool)
				for _, c := range candidates {
					seen[c.ID] = true
				}
				for _, c := range extra.Entries {
					if c != nil && !seen[c.ID] {
						candidates = append(candidates, *c)
						seen[c.ID] = true
					}
				}
			}
		}
		chosen, err = selectDiscoveredMatch(ctx, query, candidates, r.Match, func(c Candidate) (Candidate, error) {
			result, err := r.extract(ctx, "https://www.youtube.com/watch?v="+c.ID, "selected_candidate_verification", false)
			if err != nil {
				return Candidate{}, err
			}
			if result.ID != c.ID {
				return Candidate{}, errors.New("YouTube returned a different video ID")
			}
			return result.Candidate, nil
		})
		if err != nil {
			return Track{}, err
		}
	}
	if r.Match != nil {
		decision := CompareSpotify(*r.Match, chosen, r.matchPolicy())
		if decision.Status != MatchConfirmed {
			return Track{}, &MatchError{Status: decision.Status, Reason: decision.Reason}
		}
	}
	if !videoID.MatchString(chosen.ID) {
		return Track{}, ErrNoSong
	}
	if err := ctx.Err(); err != nil {
		return Track{}, err
	}
	timing.Event("candidate_selected", started, "selected", chosen.Title, "video_id", chosen.ID)
	if chosen.Artist == "" {
		chosen.Artist = chosen.Channel
	}
	return Track{Artist: chosen.Artist, Artists: chosen.Artists, Album: chosen.Album, YouTubeThumbnailURL: SafeArtworkURL(chosen.Thumbnail), identityProof: &chosen, RequestedSource: "youtube", Live: chosen.Live || chosen.LiveStatus == "is_live", VideoID: chosen.ID, Channel: chosen.Channel, Timing: timing, Title: chosen.Title, URL: "https://www.youtube.com/watch?v=" + chosen.ID, Duration: time.Duration(chosen.Duration * float64(time.Second))}, nil
}

// MetadataArgs is used only for direct videos and selected candidates whose
// lightweight metadata needs verification. Playback retains full format extraction.
func MetadataArgs() []string {
	return append(BaseArgs(), "--extractor-args", "youtube:player_client=web;player_skip=js;skip=hls,dash,translated_subs", "--ignore-no-formats-error")
}

func BaseArgs() []string {
	return []string{"--ignore-config", "--no-playlist", "--no-warnings", "--no-progress", "--no-cache-dir", "--socket-timeout", "15", "--retries", "2", "--extractor-retries", "2", "--js-runtimes", "deno"}
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// YouTubeURL validates hosts and returns only a canonical single-video URL.
// Arbitrary URLs and playlists never reach yt-dlp's generic extractors.
func YouTubeURL(s string) (string, bool, error) {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "www.youtube.com/") || strings.HasPrefix(lower, "youtube.com/") || strings.HasPrefix(lower, "youtu.be/") || strings.HasPrefix(lower, "music.youtube.com/") || strings.HasPrefix(lower, "m.youtube.com/") {
		s = "https://" + s
	}
	if !strings.Contains(s, "://") {
		return "", false, nil
	}
	bad := errors.New("Give the Bard a YouTube video URL, not a playlist or another website.")
	u, err := url.Parse(s)
	if err != nil {
		return "", true, bad
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return "", true, bad
	}
	var id string
	switch strings.ToLower(u.Hostname()) {
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else if strings.HasPrefix(u.Path, "/live/") {
			id = strings.TrimPrefix(u.Path, "/live/")
		} else if strings.HasPrefix(u.Path, "/shorts/") {
			id = strings.TrimPrefix(u.Path, "/shorts/")
		} else if strings.HasPrefix(u.Path, "/embed/") {
			id = strings.TrimPrefix(u.Path, "/embed/")
		}
	default:
		return "", true, bad
	}
	if !videoID.MatchString(id) {
		return "", true, bad
	}
	return "https://www.youtube.com/watch?v=" + id, true, nil
}

// CloneTrack detaches mutable identity fields at ownership boundaries.
func CloneTrack(t Track) Track {
	t.Artists = append([]string(nil), t.Artists...)
	if t.Explicit != nil {
		t.Explicit = new(*t.Explicit)
	}
	return t
}

// Zero means unknown (including live streams); malformed or overflowing
// provider values must never wrap into a negative time.Duration.
func validDurationSeconds(seconds float64) bool {
	return !math.IsNaN(seconds) && !math.IsInf(seconds, 0) &&
		seconds >= 0 && seconds*float64(time.Second) < float64(math.MaxInt64)
}
