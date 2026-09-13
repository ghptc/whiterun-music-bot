package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrNoSong = errors.New("The Bard could find no song worthy of playing.")

type Track struct {
	Title    string
	URL      string
	Duration time.Duration
}
type Candidate struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Channel     string   `json:"channel"`
	Uploader    string   `json:"uploader"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Duration    float64  `json:"duration"`
	Artist      string   `json:"artist"`
	Track       string   `json:"track"`
	Verified    bool     `json:"channel_is_verified"`
	Live        bool     `json:"is_live"`
	LiveStatus  string   `json:"live_status"`
}

type Resolver struct{ Binary string }

func (r Resolver) Resolve(ctx context.Context, query string) (Track, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 500 {
		return Track{}, errors.New("Give the Bard a song name or YouTube video URL (up to 500 characters).")
	}
	direct, isURL, err := YouTubeURL(query)
	if err != nil {
		return Track{}, err
	}
	target := "ytsearch10:" + query
	if isURL {
		target = direct
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	args := append(BaseArgs(), "--skip-download", "--dump-single-json", "--ignore-errors", "--", target)
	cmd := Command(ctx, r.Binary, args...)
	out := &LimitedBuffer{Limit: 8 << 20}
	stderr := &LimitedBuffer{Limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return Track{}, fmt.Errorf("YouTube resolution failed: %w: %s", err, strings.TrimSpace(string(stderr.Data)))
	}
	if out.Truncated {
		return Track{}, errors.New("YouTube metadata exceeded the size limit")
	}
	var result struct {
		Candidate
		Entries []*Candidate `json:"entries"`
	}
	if err := json.Unmarshal(out.Data, &result); err != nil {
		return Track{}, fmt.Errorf("invalid YouTube metadata: %w", err)
	}
	var chosen Candidate
	if isURL {
		chosen = result.Candidate
		if !IsMusic(chosen) {
			return Track{}, ErrNoSong
		}
	} else {
		candidates := make([]Candidate, 0, len(result.Entries))
		for _, c := range result.Entries {
			if c != nil {
				candidates = append(candidates, *c)
			}
		}
		var ok bool
		chosen, ok = Select(query, candidates)
		if !ok {
			return Track{}, ErrNoSong
		}
	}
	if !videoID.MatchString(chosen.ID) {
		return Track{}, ErrNoSong
	}
	return Track{Title: chosen.Title, URL: "https://www.youtube.com/watch?v=" + chosen.ID, Duration: time.Duration(chosen.Duration * float64(time.Second))}, nil
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
