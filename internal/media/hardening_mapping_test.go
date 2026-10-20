package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type hardeningMappings struct {
	cached                      Candidate
	readErr, saveErr, deleteErr error
	loads, saves, deletes       int
	onDelete                    func()
}

func (s *hardeningMappings) LoadMapping(context.Context, string) (Candidate, bool, error) {
	s.loads++
	return s.cached, s.cached.ID != "", s.readErr
}
func (s *hardeningMappings) SaveMapping(context.Context, string, Candidate) error {
	s.saves++
	return s.saveErr
}
func (s *hardeningMappings) DeleteMapping(context.Context, string) error {
	s.deletes++
	if s.onDelete != nil {
		s.onDelete()
	}
	return s.deleteErr
}

func TestHardeningMappingTrustAndFailureRecovery(t *testing.T) {
	failure := errors.New("injected storage failure")
	for _, tc := range []struct {
		name, extraction, want              string
		stale, uncertain, invalidID, cancel bool
		readErr, saveErr, deleteErr         error
		saves, deletes                      int
	}{
		{name: "fresh confirmed lease", want: "abcdefghijk"},
		{name: "stale revalidated", stale: true, want: "abcdefghijk", saves: 1},
		{name: "fresh but uncertain revalidated", uncertain: true, want: "abcdefghijk", saves: 1},
		{name: "refresh write failure is nonfatal", stale: true, saveErr: failure, want: "abcdefghijk", saves: 1},
		{name: "read failure discovers independently", readErr: failure, want: "lmnopqrstuv", saves: 1},
		{name: "invalid cached ID never extracted", invalidID: true, want: "lmnopqrstuv", saves: 1},
		{name: "conflicting full recording invalidates", stale: true, extraction: "mismatch", want: "lmnopqrstuv", saves: 1, deletes: 1},
		{name: "substituted full ID invalidates", stale: true, extraction: "different", want: "lmnopqrstuv", saves: 1, deletes: 1},
		{name: "failed invalidation observable but fallback safe", stale: true, extraction: "mismatch", deleteErr: failure, want: "lmnopqrstuv", saves: 1, deletes: 1},
		{name: "extraction unavailable fails closed", stale: true, extraction: "failure", deletes: 1},
		{name: "cancellation after invalidation", stale: true, extraction: "mismatch", cancel: true, deletes: 1},
		{name: "discovery write failure keeps recording", invalidID: true, saveErr: failure, want: "lmnopqrstuv", saves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
			cached := Candidate{ID: "abcdefghijk", Title: "Song", Channel: "Artist - Topic", Duration: 210, CheckedAt: time.Now()}
			if tc.stale {
				cached.CheckedAt = time.Now().Add(-time.Hour)
			}
			if tc.uncertain {
				cached.Channel = "Archive"
			}
			if tc.invalidID {
				cached.ID = "bad"
			}
			store := &hardeningMappings{cached: cached, readErr: tc.readErr, saveErr: tc.saveErr, deleteErr: tc.deleteErr}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				store.onDelete = cancel
			}
			var logs bytes.Buffer
			ctx = WithTiming(ctx, NewTiming(slog.New(slog.NewJSONHandler(&logs, nil))))
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			binary := filepath.Join(dir, "yt-dlp")
			cachedBody := `{"id":"abcdefghijk","title":"Song","channel":"Artist - Topic","duration":210}`
			if tc.extraction == "mismatch" {
				cachedBody = `{"id":"abcdefghijk","title":"Song (Live)","channel":"Artist - Topic","duration":210}`
			}
			if tc.extraction == "different" {
				cachedBody = `{"id":"12345678901","title":"Song","channel":"Artist - Topic","duration":210}`
			}
			cachedAction := "echo '" + cachedBody + "'"
			if tc.extraction == "failure" {
				cachedAction = "exit 7"
			}
			script := "#!/bin/sh\nfor arg do target=\"$arg\"; done\nprintf '%s\\n' \"$target\" >> '" + calls + "'\ncase \"$target\" in\n*watch?v=abcdefghijk) " + cachedAction + ";;\n*watch?v=lmnopqrstuv) echo '{\"id\":\"lmnopqrstuv\",\"title\":\"Song\",\"channel\":\"Artist - Topic\",\"duration\":210}';;\n*) echo '{\"entries\":[{\"id\":\"lmnopqrstuv\",\"title\":\"Song\",\"channel\":\"Artist - Topic\",\"duration\":210}]}';;\nesac\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			got, err := (Resolver{Binary: binary, Match: &original, Mappings: store}).Resolve(ctx, "Artist Song")
			if tc.want == "" {
				if err == nil || got.URL != "" {
					t.Fatalf("unsafe mapping recovery: %+v %v", got, err)
				}
				if tc.cancel && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
			} else if err != nil || got.VideoID != tc.want {
				t.Fatalf("recording changed: %+v %v", got, err)
			}
			if store.loads != 1 || store.saves != tc.saves || store.deletes != tc.deletes {
				t.Fatalf("store calls load/save/delete=%d/%d/%d", store.loads, store.saves, store.deletes)
			}
			invoked, _ := os.ReadFile(calls)
			if tc.name == "fresh confirmed lease" && len(invoked) != 0 {
				t.Fatal("fresh proof performed external extraction")
			}
			if tc.invalidID && strings.Contains(string(invoked), "watch?v=bad") {
				t.Fatal("invalid locator executed")
			}
			events := map[string][]map[string]any{}
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				var event map[string]any
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				name, _ := event["msg"].(string)
				events[name] = append(events[name], event)
			}
			lookup := events["spotify_mapping_lookup"]
			if len(lookup) != 1 || lookup[0]["success"] != (tc.readErr == nil) {
				t.Fatal("mapping lookup health incorrectly reported", lookup)
			}
			for name, want := range map[string]bool{
				"mapping_read_failed":         tc.readErr != nil,
				"mapping_write_failed":        tc.saveErr != nil,
				"mapping_invalidation_failed": tc.deleteErr != nil,
			} {
				if (len(events[name]) > 0) != want {
					t.Fatalf("operational failure telemetry %s=%v want %v\n%s", name, len(events[name]) > 0, want, logs.String())
				}
			}
		})
	}
}

func TestHardeningSpotifyIdentityEvidence(t *testing.T) {
	for _, kind := range []string{"channel", "uploader", "title prefix", "distributor", "unrelated distributor"} {
		t.Run(kind, func(t *testing.T) {
			tr, c := qualityIdentity()
			c.Artist = ""
			c.Artists = nil
			c.Channel = ""
			c.Uploader = ""
			c.Verified = false
			want := MatchConfirmed
			switch kind {
			case "channel":
				c.Channel = "Artist"
			case "uploader":
				c.Uploader = "Artist"
			case "title prefix":
				c.Title = "Artist — Song"
			case "distributor", "unrelated distributor":
				tr.ISRC = ""
				c.ISRC = ""
				c.Channel = "Distributor"
				c.Verified = true
				c.Album = "Album"
				c.Description = "Auto-generated by YouTube."
				c.Artists = []string{"Artist"}
				if kind == "unrelated distributor" {
					c.Artists = []string{"Other"}
					want = MatchRejected
				}
			}
			if got := CompareSpotify(tr, c, DefaultMatchPolicy()); got.Status != want {
				t.Fatalf("evidence=%s got=%+v", kind, got)
			}
		})
	}
	for _, seconds := range []float64{1200, 1201} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			tr, c := qualityIdentity()
			tr.Duration = 0
			c.Duration = seconds
			want := MatchConfirmed
			if seconds > 1200 {
				want = MatchRejected
			}
			if got := CompareSpotify(tr, c, DefaultMatchPolicy()); got.Status != want {
				t.Fatal(got)
			}
		})
	}
}
func TestHardeningDiscoveryDeduplicationPreservesRicherIdentity(t *testing.T) {
	rich := Candidate{ID: "abcdefghijk", Title: "richer", Channel: "Artist", Duration: 210, MusicSearch: true}
	for _, summary := range []Candidate{
		{ID: "abcdefghijk", Title: "bare"},
		{ID: "abcdefghijk", Title: "channel", Channel: "New Channel"},
		{ID: "abcdefghijk", Title: "duration", Duration: 211},
	} {
		t.Run(summary.Title, func(t *testing.T) {
			second := Candidate{ID: "lmnopqrstuv", Title: "second"}
			got := deduplicateCandidates([]*Candidate{nil, &rich, {ID: "bad"}}, []*Candidate{&summary, &second})
			want := rich
			if summary.Channel != "" || summary.Duration > 0 {
				want = summary
				want.MusicSearch = true
			}
			if len(got) != 2 || !reflect.DeepEqual(got[0], want) || !reflect.DeepEqual(got[1], second) {
				t.Fatalf("duplicate discovery lost metadata or order: %+v", got)
			}
		})
	}
}

func TestHardeningVerificationInfrastructureErrorsRemainActionable(t *testing.T) {
	tr, c := qualityIdentity()
	other := c
	other.ID = "lmnopqrstuv"
	unavailable, limited := errors.New("provider unavailable"), errors.New("provider rate limited")
	calls := 0
	got, err := selectSpotify(context.Background(), tr, []Candidate{c, other}, func(c Candidate) (Candidate, error) {
		calls++
		if c.ID == "abcdefghijk" {
			return Candidate{}, unavailable
		}
		return Candidate{}, limited
	})
	var identity *MatchError
	if calls != 2 || got.ID != "" || !errors.Is(err, unavailable) || !errors.Is(err, limited) || errors.As(err, &identity) {
		t.Fatalf("infrastructure failure disguised as identity rejection: calls=%d got=%+v err=%v", calls, got, err)
	}
}

func TestHardeningUnrelatedChannelCannotSupplyPrimaryArtist(t *testing.T) {
	tr, c := qualityIdentity()
	c.Artist = ""
	c.Artists = nil
	c.Channel = "Unrelated Artist"
	c.Uploader = ""
	c.Title = "Song"
	got := CompareSpotify(tr, c, DefaultMatchPolicy())
	if got.Status != MatchRejected {
		t.Fatalf("wrong primary artist accepted: %+v", got)
	}
}

func TestHardeningDiscoveryPreservesExtractorFailure(t *testing.T) {
	original := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	binary := filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
for arg do target="$arg"; done
case "$target" in
*watch*) echo 'provider temporarily unavailable' >&2; exit 7;;
*) echo '{"entries":[{"id":"abcdefghijk","title":"Song","channel":"Artist - Topic","duration":210}]}';;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := (Resolver{Binary: binary, Match: &original}).Resolve(context.Background(), "Artist Song")
	if got.VideoID != "" || err == nil || !strings.Contains(err.Error(), "provider temporarily unavailable") {
		t.Fatalf("provider cause replaced: %+v %v", got, err)
	}
}

func TestHardeningProvenanceWithoutISRC(t *testing.T) {
	for _, kind := range []string{"topic", "vevo", "generated release"} {
		t.Run(kind, func(t *testing.T) {
			tr, c := qualityIdentity()
			tr.ISRC = ""
			c.ISRC = ""
			c.Verified = false
			c.Channel = "Artist"
			c.Description = ""
			c.Track = ""
			switch kind {
			case "topic":
				c.Channel = "Artist - Topic"
			case "vevo":
				c.Channel = "ArtistVEVO"
			case "generated release":
				c.Artists = []string{"Artist"}
				c.Track = "Song"
				c.Description = "Auto-generated by YouTube"
			}
			if got := CompareSpotify(tr, c, DefaultMatchPolicy()); got.Status != MatchConfirmed {
				t.Fatalf("valid provenance lost: %+v", got)
			}
		})
	}
}

func TestHardeningProvisionalKnownWrongArtistIsRejected(t *testing.T) {
	for _, kind := range []string{"channel", "uploader"} {
		t.Run(kind, func(t *testing.T) {
			tr, c := qualityIdentity()
			c.Artist = ""
			c.Artists = nil
			c.Channel = ""
			c.Uploader = ""
			c.Title = "Song"
			c.MusicSearch = false
			if kind == "channel" {
				c.Channel = "Other"
			} else {
				c.Uploader = "Other"
			}
			got := rankSpotify(tr, c, true)
			if got.Reason != "primary_artist_mismatch" {
				t.Fatalf("known incompatible summary promoted for verification: %+v", got)
			}
		})
	}
}

func TestHardeningAbsentPrimaryArtistRemainsUncertain(t *testing.T) {
	tr, c := qualityIdentity()
	c.Artist = ""
	c.Artists = nil
	c.Channel = ""
	c.Uploader = ""
	c.Title = "Song"
	got := CompareSpotify(tr, c, DefaultMatchPolicy())
	if got.Status != MatchUncertain || got.Reason != "primary_artist_mismatch" {
		t.Fatalf("missing identity confused with incompatible recording: %+v", got)
	}
}
