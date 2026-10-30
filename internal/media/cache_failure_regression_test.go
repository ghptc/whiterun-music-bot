package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type brokenMappings struct {
	readFail, writeFail bool
	cached              Candidate
	deleted             bool
}

func (s *brokenMappings) LoadMapping(context.Context, string) (Candidate, bool, error) {
	if s.readFail {
		return Candidate{}, false, errors.New("read failed")
	}
	return s.cached, s.cached.ID != "", nil
}
func (s *brokenMappings) SaveMapping(context.Context, string, Candidate) error {
	if s.writeFail {
		return errors.New("write failed")
	}
	return nil
}
func (s *brokenMappings) DeleteMapping(context.Context, string) error { s.deleted = true; return nil }

func TestMappingIOFailuresDoNotInterruptConfirmedResolution(t *testing.T) {
	for _, mode := range []string{"read", "write", "both"} {
		t.Run(mode, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "yt-dlp")
			script := `#!/bin/sh
for arg do target="$arg"; done
case "$target" in
*watch?v=*) echo '{"id":"abcdefghijk","title":"Song","channel":"Artist - Topic","duration":210}';;
*) echo '{"entries":[{"id":"abcdefghijk","title":"Song","channel":"Artist - Topic","duration":210}]}';;
esac
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			store := &brokenMappings{readFail: mode != "write", writeFail: mode != "read"}
			original := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
			got, err := (Resolver{Binary: binary, Match: &original, Mappings: store}).Resolve(context.Background(), "Artist Song")
			if err != nil || got.VideoID != "abcdefghijk" {
				t.Fatal(got, err)
			}
		})
	}
}
func TestUnavailableExtractorPreservesMapping(t *testing.T) {
	store := &brokenMappings{cached: Candidate{ID: "abcdefghijk", CheckedAt: time.Now().Add(-time.Hour)}}
	original := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	_, err := (Resolver{Binary: "/nonexistent/cr002", Mappings: store, Match: &original}).Resolve(context.Background(), "Artist Song")
	if ExtractionCategory(err) != "youtube_extractor_failure" || store.deleted {
		t.Fatal(err, store.deleted)
	}
}
