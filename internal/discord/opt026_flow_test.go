package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type optSpotifyFixture struct{ calls atomic.Int32 }

func (s *optSpotifyFixture) Collection(context.Context, string, string) (media.Collection, error) {
	s.calls.Add(1)
	return media.Collection{Tracks: []media.Track{{SpotifyID: "0000000000000000000001", Title: "ValidationTone", Artist: "Artist", Artists: []string{"Artist"}, Album: "Album", Duration: 30 * time.Second, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/0000000000000000000001"}}}, nil
}

func TestOPT026FullFlowSQLiteAndMedia(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../player/testdata/local-audio/validation.flac")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "provider-mock")
	calls := filepath.Join(dir, "calls")
	dbpath := filepath.Join(dir, "catalog.db")
	script := `#!/usr/bin/env python3
import sys,json,pathlib
args=sys.argv[1:];flat='--flat-playlist' in args;metadata='--dump-single-json' in args
with open(` + fmt.Sprintf("%q", calls) + `,'a')as f:f.write(('search'if flat else 'verification'if metadata else 'media')+'\n')
if metadata:
 c={'id':'abcdefghijk','title':'ValidationTone','track':'ValidationTone','artist':'Artist','artists':['Artist'],'channel':'Artist - Topic','album':'Album','duration':30,'categories':['Music']}
 print(json.dumps({'entries':[c]}if flat else c))
else:
 print('BARD_STREAM_RESOLVED abcdefghijk',file=sys.stderr)
 sys.stdout.buffer.write(pathlib.Path(` + fmt.Sprintf("%q", fixture) + `).read_bytes())
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := testBot(t)
	spotify := &optSpotifyFixture{}
	b.resolver = media.CollectionResolver{YouTube: media.Resolver{Binary: binary, Cache: &media.SearchCache{}, Mappings: db}, Spotify: spotify, Limiter: media.NewMatchLimiter()}
	b.streamer = player.Streamer{YTDLP: binary, FFmpeg: ff}
	if err := b.ConfigureYouTubeEfficiency(media.ProviderAdmissionConfig{Concurrency: 4}, media.VerificationPrefetchHorizon); err != nil {
		t.Fatal(err)
	}
	query := "https://open.spotify.com/track/0000000000000000000001"
	previous := map[string]int{"search": 0, "verification": 0, "media": 0}
	previousSpotify := int32(0)
	for _, name := range []string{"cold_sqlite", "warm_sqlite_and_ram", "reopened_sqlite_empty_ram"} {
		if name == "reopened_sqlite_empty_ram" {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = storage.Open(dbpath)
			if err != nil {
				t.Fatal(err)
			}
			b.resolver.YouTube.Mappings = db
			b.resolver.YouTube.Cache = &media.SearchCache{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		col, _, err := b.resolver.PreparePlay(ctx, query, false)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		tr := col.Tracks[0]
		tr.ResumeOffset = 29800 * time.Millisecond
		voice := &integrityVoice{}
		if err := b.play(ctx, tr, voice); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		data, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		delta := map[string]int{}
		for _, key := range []string{"search", "verification", "media"} {
			counts[key] = strings.Count(string(data), key+"\n")
			delta[key] = counts[key] - previous[key]
		}
		if delta["media"] != 1 || voice.frames.Load() < 10 {
			t.Fatal(delta, voice.frames.Load())
		}
		if name == "cold_sqlite" {
			if delta["search"] != 1 || delta["verification"] != 1 {
				t.Fatal(delta)
			}
		} else if delta["search"] != 0 || delta["verification"] != 0 {
			t.Fatal("cache bypass", delta)
		}
		result := map[string]any{"scenario": name, "yt_dlp_mock_processes": delta, "spotify_metadata_calls": spotify.calls.Load() - previousSpotify, "voice_simulated_frames": voice.frames.Load(), "real_ffmpeg": true, "resume_offset_ms": tr.ResumeOffset.Milliseconds(), "external_http_requests": 0}
		raw, _ := json.Marshal(result)
		t.Log("MEASUREMENT " + string(raw))
		previous = counts
		previousSpotify = spotify.calls.Load()
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
