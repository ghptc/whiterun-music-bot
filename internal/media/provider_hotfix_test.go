package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderExtractionCategoriesAndSafeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		diagnostic, category string
		blocks               bool
	}{
		{"Sign in to confirm your age", "youtube_auth_required", true},
		{"Sign in to confirm you’re not a bot", "youtube_bot_challenge", true},
		{"Authentication required", "youtube_auth_required", true},
		{"HTTP Error 429: Too Many Requests", "youtube_rate_limited", true},
		{"Connection timed out", "youtube_network_timeout", true},
		{"Video unavailable", "youtube_video_unavailable", false},
		{"ERROR: Unable to extract player response", "youtube_extractor_failure", true},
	} {
		t.Run(tc.category+tc.diagnostic, func(t *testing.T) {
			// Exit zero plus valid partial JSON reproduces the masked EC2 error.
			dir := t.TempDir()
			bin := filepath.Join(dir, "yt-dlp")
			script := "#!/bin/sh\ncat <<'EOF' >&2\n" + tc.diagnostic + " Cookie: secret-cookie Authorization: secret-token https://example.test/?sig=secret-signature\nEOF\necho '{\"id\":\"MJNGXnvHYPg\",\"title\":\"Sing For The Moment\"}'\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := (Resolver{Binary: bin}).extract(context.Background(), "fixture", "spotify_candidate_verification", false)
			if ExtractionCategory(err) != tc.category || ProviderUnavailable(err) != tc.blocks {
				t.Fatalf("category lost: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "http") {
				t.Fatal("diagnostic leaked", err)
			}
			if errors.Is(err, ErrSpotifyMatch) {
				t.Fatal("provider failure became musical mismatch")
			}
		})
	}
	_, err := (func() (searchResult, error) {
		r, _ := resolverFixture(t, `{"id":"MJNGXnvHYPg","title":"Sing For The Moment"}`)
		return r.extract(context.Background(), "fixture", "spotify_playback_verification", false)
	})()
	if ExtractionCategory(err) != "youtube_metadata_unavailable" || ProviderUnavailable(err) {
		t.Fatal("missing metadata misclassified", err)
	}
	for _, arg := range MetadataArgs() {
		if arg == "--no-warnings" || arg == "--ignore-errors" {
			t.Fatal("masked errors", arg)
		}
	}
	if !errors.Is(ClassifyExtraction(nil, context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("timeout cause lost")
	}
}

func TestProviderChallengeStopsDiscoveryAtFirstFailure(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Duration: 210000000000}
	searches, verifications := 0, 0
	challenge := &ExtractionError{Category: "youtube_bot_challenge"}
	_, err := discoverSpotify(context.Background(), track, DefaultMatchPolicy(), "Artist Song", func(string, string) ([]*Candidate, error) {
		searches++
		return []*Candidate{{ID: "abcdefghijk", Title: "Song", Channel: "Artist - Topic", Duration: 210}, {ID: "lmnopqrstuv", Title: "Song", Channel: "Artist - Topic", Duration: 210}}, nil
	}, func(Candidate) (Candidate, error) { verifications++; return Candidate{}, challenge })
	if !errors.Is(err, challenge) || searches != 1 || verifications != 1 {
		t.Fatalf("cascading attempts: %d %d %v", searches, verifications, err)
	}
}

func TestMetadataDoesNotRequirePlayableFormatsWhenIdentityIsComplete(t *testing.T) {
	// Local comparison returned a usable duration despite unavailable requested
	// formats. Metadata verification must not require a download format.
	r, _ := resolverFixture(t, `{"id":"MJNGXnvHYPg","title":"Sing For The Moment","duration":340,"formats":[]}`)
	script, err := os.ReadFile(r.Binary)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\necho 'WARNING: Requested format is not available' >&2\n", 1))
	if err := os.WriteFile(r.Binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := r.extract(context.Background(), "fixture", "spotify_playback_verification", false)
	if err != nil || result.Duration != 340 {
		t.Fatal("usable metadata rejected", result, err)
	}
}

func TestSpotifySingleTrackPreservesExtractionCategory(t *testing.T) {
	for _, category := range []string{"youtube_bot_challenge", "youtube_auth_required", "youtube_metadata_unavailable", "youtube_video_unavailable"} {
		t.Run(category, func(t *testing.T) {
			original := &ExtractionError{Category: category}
			r := CollectionResolver{Spotify: fakeSpotify{Collection{Tracks: []Track{{Title: "Song", Artist: "Artist"}}}}, Search: func(context.Context, string) (Track, error) { return Track{}, original }}
			_, err := r.Resolve(context.Background(), "https://open.spotify.com/track/1234567890123456789012")
			if !errors.Is(err, original) || errors.Is(err, ErrSpotifyMatch) {
				t.Fatal("single-track wrapper lost provider cause", err)
			}
		})
	}
}

func TestSynchronousSpotifyCollectionStopsOnProviderChallenge(t *testing.T) {
	tracks := make([]Track, 100)
	for i := range tracks {
		tracks[i] = Track{Title: "Song", Artist: "Artist"}
	}
	var calls atomic.Int32
	original := &ExtractionError{Category: "youtube_bot_challenge"}
	r := CollectionResolver{Spotify: fakeSpotify{Collection{Tracks: tracks}}, Search: func(context.Context, string) (Track, error) { calls.Add(1); return Track{}, original }}
	_, err := r.Resolve(context.Background(), "https://open.spotify.com/playlist/1234567890123456789012")
	if !errors.Is(err, original) || calls.Load() > 4 {
		t.Fatal("synchronous worker cascade", calls.Load(), err)
	}
}

func TestYouTubePlaylistPartialJSONCannotMaskProviderChallenge(t *testing.T) {
	r, _ := resolverFixture(t, `{"entries":[{"id":"abcdefghijk","title":"Song"}]}`)
	script, err := os.ReadFile(r.Binary)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\necho \"WARNING: Sign in to confirm you're not a bot; Cookie: secret\" >&2\n", 1))
	if err := os.WriteFile(r.Binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://www.youtube.com/playlist?list=PLtest")
	if ExtractionCategory(err) != "youtube_bot_challenge" || strings.Contains(err.Error(), "secret") {
		t.Fatal("partial playlist masked challenge", err)
	}
}
