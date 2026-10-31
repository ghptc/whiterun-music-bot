// Isolated metadata/source probe: no Discord connection, queue or deployment.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type proof struct{ candidate media.Candidate }

func (*proof) LoadMapping(context.Context, string) (media.Candidate, bool, error) {
	return media.Candidate{}, false, nil
}
func (p *proof) SaveMapping(_ context.Context, _ string, c media.Candidate) error {
	p.candidate = c
	return nil
}
func (*proof) DeleteMapping(context.Context, string) error { return nil }

type sink struct {
	cancel context.CancelFunc
	frames int
}

func (s *sink) WriteFrame(context.Context, []byte) error {
	s.frames++
	if s.frames == 50 {
		s.cancel()
	}
	return nil
}
func (*sink) Speaking(context.Context, bool) error { return nil }
func (*sink) Close(context.Context)                {}
func main() {
	only := flag.String("only", "", "optional Spotify ID among the four incident titles")
	catalog := flag.String("catalog", "", "optional existing catalog: write only after verified match and 50 audio frames")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	spotify := &media.Spotify{Log: log, ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN"), Market: os.Getenv("SPOTIFY_MARKET")}
	collection, err := spotify.CollectionForPlayback(ctx, "playlist", "35m4wmHSlcS2RNsafX4dEg")
	if err != nil {
		log.Error("probe_playlist_failed", "error", err)
		os.Exit(1)
	}
	ids := map[string]bool{"5PX4uS1LqlWEPL69phPVQQ": true, "1xsoW8gAQToUbiokm6Hn5Q": true, "3USOTQdPtZlgVurwaqsdI5": true, "0ffz9KBdCb7oJkSK0W7bbf": true}
	if *only != "" {
		for id := range ids {
			if id != *only {
				delete(ids, id)
			}
		}
	}
	type row struct {
		SpotifyID, Title, VideoID, Error string
		Position, Frames                 int
		ElapsedMS                        int64
		Saved                            bool
		Decision                         media.MatchDecision
	}
	result := struct {
		Playlist  string
		Positions int
		Results   []row
	}{Playlist: "35m4wmHSlcS2RNsafX4dEg", Positions: len(collection.Tracks)}
	var db *storage.DB
	if *catalog != "" {
		db, err = storage.Open(*catalog)
		if err != nil {
			log.Error("catalog_open_failed", "error", err)
			os.Exit(1)
		}
		defer db.Close()
	}
	for i, tr := range collection.Tracks {
		if !ids[tr.SpotifyID] {
			continue
		}
		delete(ids, tr.SpotifyID)
		started := time.Now()
		r := row{SpotifyID: tr.SpotifyID, Position: i + 1}
		tr, err = spotify.TrackMetadata(ctx, tr.SpotifyID)
		if err == nil {
			r.Title = tr.Title
			evidence := &proof{}
			resolver := media.Resolver{Binary: "yt-dlp", Match: &tr, Mappings: evidence}
			trace := media.WithTiming(ctx, media.NewTiming(log.With("spotify_track_id", tr.SpotifyID)))
			var selected media.Track
			selected, err = resolver.Resolve(trace, tr.Artist+" "+tr.Title)
			r.VideoID = selected.VideoID
			if err == nil {
				r.Decision = media.CompareSpotify(tr, evidence.candidate, media.DefaultMatchPolicy())
				audioCtx, stop := context.WithTimeout(trace, 45*time.Second)
				output := &sink{cancel: stop}
				streamErr := (player.Streamer{YTDLP: "yt-dlp", FFmpeg: "ffmpeg"}).Play(audioCtx, selected, output)
				stop()
				r.Frames = output.frames
				if output.frames < 50 {
					err = streamErr
					if err == nil {
						err = fmt.Errorf("source ended before 50 frames")
					}
				}
				if err != nil && output.frames < 50 {
					alternative, frames, alternativeErr := findAlternative(trace, tr, selected.VideoID, log)
					if alternativeErr != nil {
						log.Info("fix025_alternative_failed", "reason", media.SafeDiagnostic([]byte(alternativeErr.Error())))
					}
					if alternativeErr == nil {
						evidence.candidate = alternative
						r.VideoID, r.Frames, err = alternative.ID, frames, nil
						r.Decision = media.CompareSpotify(tr, alternative, media.DefaultMatchPolicy())
					}
				}
				if err == nil && db != nil {
					err = db.SaveMapping(ctx, media.MappingKey(tr, nil), evidence.candidate)
					if err == nil {
						err = db.SaveCatalog(ctx, tr)
					}
					r.Saved = err == nil
				}
			}
		}
		if err != nil {
			r.Error = err.Error()
		}
		r.ElapsedMS = time.Since(started).Milliseconds()
		result.Results = append(result.Results, r)
	}
	for id := range ids {
		result.Results = append(result.Results, row{SpotifyID: id, Error: "not found in original playlist manifest"})
	}
	json.NewEncoder(os.Stdout).Encode(result)
}

// At most six unique alternatives from one bounded search. This diagnostic
// never relaxes the production recording-identity policy.
func findAlternative(ctx context.Context, tr media.Track, rejected string, log *slog.Logger) (media.Candidate, int, error) {
	extract := func(target string, flat bool, out any) error {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		args := media.MetadataArgs()
		if flat {
			args = append(media.BaseArgs(), "--flat-playlist", "--playlist-end", "10")
		}
		args = append(args, "--skip-download", "--dump-single-json", "--ignore-errors", "--", target)
		cmd := media.Command(bounded, "yt-dlp", args...)
		stdout, stderr := &media.LimitedBuffer{Limit: 8 << 20}, &media.LimitedBuffer{Limit: 8192}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("alternative extraction failed: %s", media.SafeDiagnostic(stderr.Data))
		}
		if stdout.Truncated {
			return fmt.Errorf("alternative metadata exceeded limit")
		}
		return json.Unmarshal(stdout.Data, out)
	}
	var pool struct {
		Entries []*media.Candidate `json:"entries"`
	}
	if err := extract("ytsearch10:"+tr.Artist+" "+tr.Title+" official audio", true, &pool); err != nil {
		return media.Candidate{}, 0, err
	}
	seen := map[string]bool{rejected: true}
	checked := 0
	for _, candidate := range pool.Entries {
		if candidate == nil || seen[candidate.ID] || len(candidate.ID) != 11 {
			continue
		}
		seen[candidate.ID] = true
		checked++
		if checked > 6 {
			break
		}
		var full media.Candidate
		err := extract("https://www.youtube.com/watch?v="+candidate.ID, false, &full)
		decision := media.CompareSpotify(tr, full, media.DefaultMatchPolicy())
		log.Info("fix025_alternative_checked", "video_id", candidate.ID, "decision", decision.Status, "reason", decision.Reason, "extracted", err == nil)
		if err != nil || full.ID != candidate.ID || decision.Status != media.MatchConfirmed {
			continue
		}
		audioCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		output := &sink{cancel: stop}
		selected := tr
		selected.VideoID = full.ID
		selected.URL = "https://www.youtube.com/watch?v=" + full.ID
		streamErr := (player.Streamer{YTDLP: "yt-dlp", FFmpeg: "ffmpeg"}).Play(audioCtx, selected, output)
		stop()
		log.Info("fix025_alternative_audio", "video_id", full.ID, "frames", output.frames)
		if output.frames >= 50 {
			full.CheckedAt = time.Now()
			return full, output.frames, nil
		}
		_ = streamErr
	}
	return media.Candidate{}, 0, fmt.Errorf("no independently verified playable alternative in bounded search")
}
