package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Query, Want string
		Candidates        []Candidate
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			got, ok := Select(tt.Query, tt.Candidates)
			if !ok || got.ID != tt.Want {
				t.Fatalf("Select: got %s want %s", got.ID, tt.Want)
			}
			got, err := selectDiscovered(context.Background(), tt.Query, tt.Candidates, func(Candidate) (Candidate, error) { t.Fatal("complete fixture re-extracted"); return Candidate{}, nil })
			if err != nil || got.ID != tt.Want {
				t.Fatalf("discovery: %s %v", got.ID, err)
			}
		})
	}
}
func TestSpotifyStructuredMatchAndCacheIsolation(t *testing.T) {
	fixture := `{"entries":[{"id":"wrong000001","title":"Arctic Monkeys - 505 live","artist":"Arctic Monkeys","duration":253,"categories":["Music"]},{"id":"wrong000002","title":"505","channel":"Other Artist - Topic","artist":"Other Artist","duration":253},{"id":"wrong000003","title":"Arctic Monkeys - 505 extended","channel":"Arctic Monkeys","duration":600,"categories":["Music"]},{"id":"correct0001","title":"505","channel":"Arctic Monkeys - Topic","artist":"Arctic Monkeys","track":"505","duration":253}]}`
	r, _ := candidateResolverFixture(t, fixture)
	r.Cache = &SearchCache{}
	// A successful free-text cache entry must not bypass structured matching.
	r.Cache.put(normalizeQuery("Arctic Monkeys 505"), Track{VideoID: "wrong"}, time.Now())
	r.Match = &Track{Title: "505", Artist: "Arctic Monkeys", Duration: 253 * time.Second}
	got, err := r.Resolve(context.Background(), "Arctic Monkeys 505")
	if err != nil || got.VideoID != "correct0001" {
		t.Fatal(got, err)
	}
	// Confirm the strict result has its own usable cache entry.
	if err := os.Remove(r.Binary); err != nil {
		t.Fatal(err)
	}
	got, err = r.Resolve(context.Background(), "Arctic Monkeys 505")
	if err != nil || got.VideoID != "correct0001" {
		t.Fatal(got, err)
	}
}
func TestSpotifyRejectsWrongTitleAndArtist(t *testing.T) {
	match := &Track{Title: "505", Artist: "Arctic Monkeys"}
	for _, candidate := range []Candidate{song("Another Song", "Arctic Monkeys - Topic"), song("505", "Other Artist - Topic"), song("Arctic Monkeys - 505 live", "Archive")} {
		_, err := selectDiscoveredMatch(context.Background(), "Arctic Monkeys 505", []Candidate{candidate}, match, func(Candidate) (Candidate, error) { t.Fatal("wrong metadata verified"); return Candidate{}, nil })
		if !errors.Is(err, ErrNoSong) {
			t.Fatal(err)
		}
	}
}

func TestCanonicalDiscoveryBroadensSearchWhenReleaseMissing(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "yt-dlp")
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
for arg do target="$arg"; done
printf '%s\n' "$target" >> '` + calls + `'
case "$target" in
*'official audio')
echo '{"entries":[{"id":"correct0001","title":"505","channel":"Arctic Monkeys - Topic","duration":253}]}'
;;
*)
echo '{"entries":[{"id":"random00001","title":"Arctic Monkeys - 505","channel":"Pizza Music","channel_is_verified":true,"duration":253,"categories":["Music"]},{"id":"other000001","title":"I Wanna Be Yours","channel":"Arctic Monkeys - Topic","duration":180}]}'
;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := (Resolver{Binary: binary}).Resolve(context.Background(), "505 Arctic Monkeys")
	if err != nil || got.VideoID != "correct0001" {
		t.Fatal(got, err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "ytsearch10:") != 2 {
		t.Fatalf("unexpected subprocesses: %s", data)
	}
}

func TestSpotifyAlbumVariantAndConflictingArtist(t *testing.T) {
	track := Track{Title: "Nutshell", Artist: "Alice in Chains", Album: "MTV Unplugged", Duration: 260 * time.Second}
	live := song("Alice in Chains - Nutshell live unplugged", "Alice in Chains")
	if !strings.Contains(spotifyQuery(track), "unplugged") || !matchesMetadata(&track, live) {
		t.Fatal("lost album version")
	}
	studio := track
	studio.Album = "Jar of Flies"
	if matchesMetadata(&studio, live) {
		t.Fatal("accepted unsolicited unplugged")
	}
	wrong := song("Alice in Chains - Nutshell", "Other Singer")
	wrong.Artist = "Other Singer"
	if matchesMetadata(&studio, wrong) {
		t.Fatal("ignored conflicting artist metadata")
	}
}
