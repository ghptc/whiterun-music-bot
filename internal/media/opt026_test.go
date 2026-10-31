package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestOPT026FiveSharedProofConsumersAndRenewal(t *testing.T) {
	r, calls := accessProbe(t, false)
	ctx := context.Background()
	original := accessTrack(1)
	tr, err := r.MatchSpotify(ctx, original, true)
	if err != nil {
		t.Fatal(err)
	}
	// Expire the shared raw cache and caller proof, as if ten minutes passed.
	r.YouTube.Cache.mu.Lock()
	for key, e := range r.YouTube.Cache.entries {
		e.expires = time.Now().Add(-time.Second)
		r.YouTube.Cache.entries[key] = e
	}
	r.YouTube.Cache.mu.Unlock()
	tr.identityProof.CheckedAt = time.Now().Add(-11 * time.Minute)
	store := r.YouTube.Mappings.(*accessProbeMappings)
	store.mu.Lock()
	for key, c := range store.items {
		c.CheckedAt = tr.identityProof.CheckedAt
		store.items[key] = c
	}
	store.mu.Unlock()
	_, before := accessCounts(t, calls)
	var wg sync.WaitGroup
	results := make(chan Track, 5)
	for range 5 {
		wg.Go(func() {
			v, err := r.YouTube.VerifiedSpotify(ctx, tr)
			if err != nil {
				t.Error(err)
				return
			}
			results <- v
		})
	}
	wg.Wait()
	close(results)
	var got []Track
	for v := range results {
		got = append(got, v)
	}
	if len(got) != 5 {
		t.Fatal(len(got))
	}
	for _, v := range got {
		if v.VideoID != tr.VideoID || v.Title != tr.Title || v.identityProof == nil || !freshIdentity(*v.identityProof, time.Now()) {
			t.Fatal(v)
		}
	}
	_, after := accessCounts(t, calls)
	if after-before != 1 {
		t.Fatal("duplicate refresh", after-before)
	}
	got[0].identityProof.Artists[0] = "Impostor"
	got[0].Artists[0] = "Impostor"
	if got[1].Artists[0] != "Artist" || got[1].identityProof.Artists[0] != "Artist" {
		t.Fatal("shared mutable proof")
	}
	if err := r.YouTube.VerifySpotify(ctx, tr); err != nil {
		t.Fatal(err)
	}
	_, last := accessCounts(t, calls)
	if last != after {
		t.Fatal("sequential refresh repeated")
	}
	persisted, hit, err := r.YouTube.Mappings.LoadMapping(ctx, MappingKey(original, nil))
	if err != nil || !hit || !freshIdentity(persisted, time.Now()) {
		t.Fatal("renewal not persisted", hit, err)
	}
	wrong := tr
	wrong.Title += " Live"
	if _, err := r.YouTube.VerifiedSpotify(ctx, wrong); err == nil {
		t.Fatal("one caller's acceptance contaminated another")
	}
	if _, ok := r.YouTube.Cache.get("spotify:"+MappingKey(wrong, nil), time.Now()); ok {
		t.Fatal("failed comparison cached as match")
	}
}

func blockingProof(t *testing.T) (Resolver, string, string) {
	t.Helper()
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	pid := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "extractor")
	script := "#!/bin/sh\necho $$ > '" + pid + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.01; done\necho '{\"id\":\"abcdefghijk\",\"title\":\"Song\",\"track\":\"Song\",\"artists\":[\"Artist\"],\"channel\":\"Artist - Topic\",\"duration\":210}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return Resolver{Binary: bin, Cache: &SearchCache{}}, release, pid
}
func waitOPT026(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("test synchronization timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestOPT026SharedProofCancellation(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(strconv.FormatBool(all), func(t *testing.T) {
			r, release, pidFile := blockingProof(t)
			const consumers = 5
			cancels := make([]context.CancelFunc, consumers)
			errs := make(chan error, consumers)
			for i := range consumers {
				ctx, cancel := context.WithCancel(context.Background())
				cancels[i] = cancel
				defer cancel()
				go func() { _, err := r.rawVideo(ctx, "abcdefghijk", "test"); errs <- err }()
			}
			waitOPT026(t, func() bool {
				r.Cache.mu.Lock()
				defer r.Cache.mu.Unlock()
				for _, flight := range r.Cache.flights {
					if flight.waiters == consumers {
						return true
					}
				}
				return false
			})
			waitOPT026(t, func() bool { _, err := os.Stat(pidFile); return err == nil })
			cancels[0]()
			if err := <-errs; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if all {
				for _, cancel := range cancels[1:] {
					cancel()
				}
				for range consumers - 1 {
					if err := <-errs; !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				}
				data, err := os.ReadFile(pidFile)
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				waitOPT026(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
			} else {
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				for range consumers - 1 {
					if err := <-errs; err != nil {
						t.Fatal("remaining consumer canceled", err)
					}
				}
			}
		})
	}
}
func TestOPT026ProofFailureAndContextIsolation(t *testing.T) {
	r, calls := accessProbe(t, true)
	if _, err := r.YouTube.rawVideo(context.Background(), "00000000001", "test"); GlobalProviderFailure(err) != "youtube_bot_challenge" {
		t.Fatal(err)
	}
	if len(r.YouTube.Cache.entries) != 0 {
		t.Fatal("failed extraction poisoned raw cache")
	}
	// Same cache, different immutable extractor context.
	good, _ := accessProbe(t, false)
	good.YouTube.Cache = r.YouTube.Cache
	if _, err := good.YouTube.rawVideo(context.Background(), "00000000001", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := good.YouTube.rawVideo(context.Background(), "00000000002", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.YouTube.rawVideo(context.Background(), "00000000001", "test"); err == nil {
		t.Fatal("binary contexts shared")
	}
	_, v := accessCounts(t, calls)
	if v != 2 {
		t.Fatal(v)
	}
	// A temporary failure must not poison a later success in the SAME context.
	script, err := os.ReadFile(r.YouTube.Binary)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), "if not flat and True:", "if not flat and False:", 1))
	if err := os.WriteFile(r.YouTube.Binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.YouTube.rawVideo(context.Background(), "00000000001", "test"); err != nil {
		t.Fatal(err)
	}
	// Case-sensitive IDs never share a proof, even when returned metadata is bad.
	fixture, _ := resolverFixture(t, `{"id":"Abcdefghijk","title":"Song","duration":210}`)
	fixture.Cache = &SearchCache{}
	if _, err := fixture.rawVideo(context.Background(), "Abcdefghijk", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.rawVideo(context.Background(), "abcdefghijk", "test"); !errors.Is(err, errVideoIdentityMismatch) {
		t.Fatal(err)
	}
}

func TestOPT026NamedIdentityCorpus(t *testing.T) {
	positives, negatives := 0, 0
	for _, version := range []string{"", "Live", "Remix", "Cover", "Radio Edit", "Extended Mix", "Remastered 2021", "Acoustic", "Instrumental"} {
		tr, c := qualityIdentity()
		if version != "" {
			tr.Title += " (" + version + ")"
			c.Title = tr.Title
			c.Track = tr.Title
		}
		if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchConfirmed {
			t.Fatalf("positive %s: %+v", version, d)
		}
		positives++
		bad := c
		if version == "" {
			bad.Title += " Live"
			bad.Track = bad.Title
		} else {
			bad.Title = "Song"
			bad.Track = "Song"
		}
		if d := CompareSpotify(tr, bad, DefaultMatchPolicy()); d.Status == MatchConfirmed {
			t.Fatalf("negative %s: %+v", version, d)
		}
		negatives++
	}
	for _, tc := range []struct {
		title, artist string
		credits       []string
		seconds       int
	}{
		{"Pursuit Of Happiness - Extended Steve Aoki Remix", "Kid Cudi", []string{"Kid Cudi", "MGMT", "Ratatat", "Steve Aoki"}, 374},
		{"Heads Will Roll - A-Trak Remix", "Yeah Yeah Yeahs", []string{"Yeah Yeah Yeahs", "A-Trak"}, 384},
		{"Beelzeboss", "Tenacious D", []string{"Tenacious D"}, 365},
		{"Fuck Her Gently", "Tenacious D", []string{"Tenacious D"}, 123},
	} {
		tr := Track{Title: tc.title, Artist: tc.artist, Artists: tc.credits, Album: "Album", Duration: time.Duration(tc.seconds) * time.Second, Explicit: new(true)}
		c := Candidate{ID: "abcdefghijk", Title: tc.title, Track: tc.title, Artist: tc.artist, Artists: append([]string(nil), tc.credits...), Album: "Album", Channel: tc.artist + " - Topic", Duration: float64(tc.seconds), Explicit: new(true)}
		if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchConfirmed {
			t.Fatalf("known positive %s: %+v", tc.title, d)
		}
		positives++
		for _, change := range []func(*Candidate){
			func(c *Candidate) { c.Title += " Live"; c.Track = c.Title },
			func(c *Candidate) {
				c.Artist = "Other Artist"
				c.Artists = []string{"Other Artist"}
				c.Channel = "Other Artist"
			},
			func(c *Candidate) { c.Explicit = new(false) },
			func(c *Candidate) { c.Duration = 3600; c.Title += " Full Album"; c.Track = c.Title },
			func(c *Candidate) { c.Title = "Song Other Producer Remix"; c.Track = c.Title },
		} {
			bad := c
			change(&bad)
			if d := CompareSpotify(tr, bad, DefaultMatchPolicy()); d.Status == MatchConfirmed {
				t.Fatalf("known negative %s: %+v", tc.title, d)
			}
			negatives++
		}
	}
	tr, c := qualityIdentity()
	tr.Artists = []string{"Artist", "Guest"}
	c.Artists = []string{"Artist", "Guest"}
	if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchConfirmed {
		t.Fatal(d)
	}
	positives++
	for _, artists := range [][]string{{"Artist"}, {"Artist", "Wrong Guest"}} {
		bad := c
		bad.Artists = artists
		if d := CompareSpotify(tr, bad, DefaultMatchPolicy()); d.Status == MatchConfirmed {
			t.Fatal(d)
		}
		negatives++
	}
	c.Duration = 0
	if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchUncertain {
		t.Fatal("missing evidence accepted", d)
	}
	t.Logf("identity corpus positives=%d negatives=%d false_positives=0", positives, negatives)
}

func TestOPT026UnavailableAndAgeRestrictedProofsPreserved(t *testing.T) {
	for _, message := range []string{"Sign in to confirm your age", "Video unavailable"} {
		tr, c := qualityIdentity()
		tr.RequestedSource = "spotify"
		tr.SpotifyID = "identity"
		tr.VideoID = c.ID
		tr.URL = "https://www.youtube.com/watch?v=" + c.ID
		tr.ResumeOffset = 40 * time.Second
		store := &accessProbeMappings{items: map[string]Candidate{MappingKey(tr, nil): c}}
		dir := t.TempDir()
		bin := filepath.Join(dir, "extractor")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '"+message+"' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		r := Resolver{Binary: bin, Cache: &SearchCache{}, Mappings: store}
		_, err := r.VerifiedSpotify(context.Background(), tr)
		if err == nil {
			t.Fatal("unauthorized access accepted")
		}
		if GlobalProviderFailure(err) != "" {
			t.Fatal("video-specific failure blocked all guilds")
		}
		if tr.URL != "https://www.youtube.com/watch?v="+c.ID || tr.ResumeOffset != 40*time.Second {
			t.Fatal("caller checkpoint changed")
		}
		_, ok, _ := store.LoadMapping(context.Background(), MappingKey(tr, nil))
		if !ok || len(r.Cache.entries) != 0 {
			t.Fatal("proof identity discarded or false success cached")
		}
	}
}

func TestOPT026ProofCoversExpectedPlayback(t *testing.T) {
	r, calls := accessProbe(t, false)
	ctx := context.Background()
	original := accessTrack(1)
	if _, err := r.MatchSpotify(ctx, original, true); err != nil {
		t.Fatal(err)
	}
	age := time.Now().Add(-7 * time.Minute)
	r.YouTube.Cache.mu.Lock()
	for key, e := range r.YouTube.Cache.entries {
		if e.track.identityProof != nil {
			e.track.identityProof.CheckedAt = age
		}
		e.expires = age.Add(identityLease)
		r.YouTube.Cache.entries[key] = e
	}
	r.YouTube.Cache.mu.Unlock()
	store := r.YouTube.Mappings.(*accessProbeMappings)
	store.mu.Lock()
	for key, c := range store.items {
		c.CheckedAt = age
		store.items[key] = c
	}
	store.mu.Unlock()
	beforeS, beforeV := accessCounts(t, calls)
	forecast := WithPlaybackEstimate(ctx, 6*time.Minute)
	renewed, err := r.MatchSpotify(forecast, original, false)
	if err != nil {
		t.Fatal(err)
	}
	s, v := accessCounts(t, calls)
	if s != beforeS || v-beforeV != 1 {
		t.Fatal("forecast either rediscovers or reuses expired proof", s, v)
	}
	if renewed.identityProof == nil || !proofCovers(forecast, *renewed.identityProof) {
		t.Fatal("proof will expire before playback")
	}
}
