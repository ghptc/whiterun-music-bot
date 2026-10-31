package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"whiterun/internal/media"
)

// evidenceStore captures successful proofs without loading or writing a database.
type evidenceStore struct{ candidate media.Candidate }

func (s *evidenceStore) LoadMapping(context.Context, string) (media.Candidate, bool, error) {
	return media.Candidate{}, false, nil
}
func (s *evidenceStore) SaveMapping(_ context.Context, _ string, c media.Candidate) error {
	s.candidate = c
	return nil
}
func (s *evidenceStore) DeleteMapping(context.Context, string) error { return nil }
func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	s := &media.Spotify{Log: log, ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN"), Market: os.Getenv("SPOTIFY_MARKET")}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	c, err := s.Collection(ctx, "playlist", "37i9dQZF1DZ06evO1sJmec")
	if err != nil {
		log.Error("probe_failed", "error", err)
		os.Exit(1)
	}
	type result struct {
		Position                                 int
		Title, Artist, SpotifyID, VideoID, Error string
		ElapsedMS                                int64
		Source                                   media.Track
		Selected                                 media.Candidate
		Decision                                 media.MatchDecision
	}
	results := make([]result, len(c.Tracks))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				t := c.Tracks[i]
				start := time.Now()
				evidence := &evidenceStore{}
				r := media.Resolver{Binary: "yt-dlp", Match: &t, Mappings: evidence}
				trace := media.WithTiming(ctx, media.NewTiming(log.With("position", i+1, "spotify_track_id", t.SpotifyID)))
				got, e := r.Resolve(trace, t.Artist+" "+t.Title)
				row := result{Source: t, Selected: evidence.candidate, Position: i + 1, Title: t.Title, Artist: t.Artist, SpotifyID: t.SpotifyID, VideoID: got.VideoID, ElapsedMS: time.Since(start).Milliseconds()}
				if e != nil {
					row.Error = e.Error()
				} else {
					row.Decision = media.CompareSpotify(t, evidence.candidate, media.DefaultMatchPolicy())
				}
				results[i] = row
			}
		}()
	}
	for i := range c.Tracks {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	json.NewEncoder(os.Stdout).Encode(results)
}
