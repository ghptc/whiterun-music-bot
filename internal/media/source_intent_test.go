package media

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDirectSourceIntent(t *testing.T) {
	for _, tc := range []struct{ name, extra, url string }{
		{"unofficial", `"duration":12,"categories":["Comedy"]`, "https://youtu.be/abcdefghijk?list=PLcontext"},
		{"live", `"is_live":true,"live_status":"is_live"`, "https://youtube.com/live/abcdefghijk"},
		{"nonmusic", `"duration":2700,"categories":["News"]`, "https://youtube.com/watch?v=abcdefghijk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, args := resolverFixture(t, `{"id":"abcdefghijk","title":"Weird unofficial reaction",`+tc.extra+`}`)
			collection, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), tc.url)
			if err != nil || len(collection.Tracks) != 1 {
				t.Fatal(collection, err)
			}
			track := collection.Tracks[0]
			if track.VideoID != "abcdefghijk" || track.Live != (tc.name == "live") {
				t.Fatal(track)
			}
			raw, _ := os.ReadFile(args)
			if strings.Contains(string(raw), "ytsearch") || strings.Contains(string(raw), "--yes-playlist") {
				t.Fatal(string(raw))
			}
		})
	}
}
func TestPlaylistPreservesIDsOrderDuplicatesAndLive(t *testing.T) {
	r, args := resolverFixture(t, `{"entries":[{"id":"abcdefghijk","title":"Unofficial cover"},{"id":"lmnopqrstuv","title":"Live","is_live":true},{"id":"abcdefghijk","title":"Unofficial cover"}]}`)
	c, err := (CollectionResolver{YouTube: r}).Resolve(context.Background(), "https://youtube.com/playlist?list=PLtest")
	if err != nil || len(c.Tracks) != 3 {
		t.Fatal(c, err)
	}
	for i, id := range []string{"abcdefghijk", "lmnopqrstuv", "abcdefghijk"} {
		if c.Tracks[i].VideoID != id || !c.Tracks[i].VerifyBeforePlay {
			t.Fatal(c)
		}
	}
	if !c.Tracks[1].Live {
		t.Fatal("lost live intent")
	}
	raw, _ := os.ReadFile(args)
	if strings.Contains(string(raw), "ytsearch") || !strings.Contains(string(raw), "--flat-playlist") {
		t.Fatal(string(raw))
	}
}
func TestSpotifyRecordingFidelity(t *testing.T) {
	base := Track{Title: "Song Name", Artist: "Artist", Artists: []string{"Artist"}, Duration: 210 * time.Second}
	candidate := func(title, channel string) Candidate {
		return Candidate{ID: "abcdefghijk", Title: title, Channel: channel, Duration: 210, Categories: []string{"Music"}}
	}
	for _, tc := range []struct{ name, title, badTitle, goodTitle string }{
		{"studio over live", "Song Name", "Artist - Song Name Live", "Artist - Song Name"},
		{"requested live", "Song Name - Live", "Artist - Song Name", "Artist - Song Name Live"},
		{"studio over remix", "Song Name", "Artist - Song Name Remix", "Artist - Song Name"},
		{"requested remix", "Song Name (Remix)", "Artist - Song Name", "Artist - Song Name Remix"},
		{"remaster", "Song Name - 2011 Remaster", "Artist - Song Name 2009 Remastered", "Artist - Song Name (Remastered 2011)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			track := base
			track.Title = tc.title
			wrong := candidate(tc.badTitle, "Artist")
			wrong.Verified = true
			good := candidate(tc.goodTitle, "Artist - Topic")
			good.ID = "lmnopqrstuv"
			got, err := selectSpotify(context.Background(), track, []Candidate{wrong, good}, func(c Candidate) (Candidate, error) { return c, nil })
			if err != nil || got.ID != good.ID {
				t.Fatal(got, err, rankSpotify(track, wrong, false), rankSpotify(track, good, false))
			}
		})
	}
	for _, suffix := range []string{"Live", "Concert", "Remix", "Acoustic", "Slowed + Reverb", "Sped Up", "Instrumental", "Karaoke", "Nightcore", "8D", "Bass Boosted", "Reaction", "Cover", "Tutorial", "Compilation", "Full Album", "Mashup"} {
		t.Run("reject "+suffix, func(t *testing.T) {
			c := candidate("Artist - Song Name "+suffix, "Artist")
			c.Verified = true
			if b := rankSpotify(base, c, false); b.Reason == "accepted" {
				t.Fatal(b)
			}
		})
	}
	for _, duration := range []float64{260, 2700, 3} {
		c := candidate("Artist - Song Name", "Artist - Topic")
		c.Duration = duration
		if b := rankSpotify(base, c, false); b.Reason != "duration_mismatch" {
			t.Fatal(b)
		}
	}
	official := candidate("Song Name", "Artist - Topic")
	random := candidate("Artist - Song Name OFFICIAL AUDIO", "Random uploader")
	random.ID = "lmnopqrstuv"
	got, err := selectSpotify(context.Background(), base, []Candidate{random, official}, func(c Candidate) (Candidate, error) { return c, nil })
	if err != nil || got.ID != official.ID {
		t.Fatal(got, err)
	}
	if class, bonus := sourceClass(base, random); class != "other" || bonus != 0 {
		t.Fatal("fake authority", class, bonus)
	}
	unrelated := candidate("Another Song", "Artist")
	if _, err := selectSpotify(context.Background(), base, []Candidate{unrelated}, func(c Candidate) (Candidate, error) { return c, nil }); !errors.Is(err, ErrSpotifyMatch) {
		t.Fatal(err)
	}
}
func TestSpotifyUnicodeCreditsAndFeatures(t *testing.T) {
	for _, tc := range []struct {
		track     Track
		candidate Candidate
	}{
		{Track{Title: "Déjà Vu", Artist: "Beyoncé"}, Candidate{Title: "Beyonce - Déjà Vu", Channel: "Beyonce - Topic"}},
		{Track{Title: "Don't Stop!", Artist: "Artist"}, Candidate{Title: "Artist — Don’t Stop", Channel: "Artist"}},
		{Track{Title: "Song (feat. Guest)", Artist: "Artist", Artists: []string{"Artist", "Guest"}}, Candidate{Title: "Artist - Song ft Guest", Artists: []string{"Artist", "Guest"}, Channel: "Artist - Topic"}},
		{Track{Title: "Song (featuring Guest)", Artist: "Artist", Artists: []string{"Artist", "Guest"}}, Candidate{Title: "Song", Track: "Song", Artists: []string{"Artist", "Guest"}, Channel: "Artist - Topic"}},
		{Track{Title: "Song", Artist: "2 Live Crew"}, Candidate{Title: "2 Live Crew - Song", Channel: "2 Live Crew - Topic"}},
	} {
		tc.candidate.ID = "abcdefghijk"
		tc.candidate.Duration = 210
		if b := rankSpotify(tc.track, tc.candidate, false); b.Reason != "accepted" {
			t.Fatalf("%+v: %+v", tc, b)
		}
	}
	t1 := Track{Title: "Song", Artist: "Artist", Artists: []string{"Artist", "Guest"}}
	wrong := Candidate{ID: "abcdefghijk", Title: "Song", Artists: []string{"Artist", "Wrong guest"}, Duration: 210}
	if b := rankSpotify(t1, wrong, false); b.Reason != "credited_artist_mismatch" {
		t.Fatal(b)
	}
}
func TestSpotifyCandidatePoolsAndVerificationBound(t *testing.T) {
	fixture := `{"entries":[{"id":"correct0001","title":"Song Name","channel":"Artist - Topic","duration":210},{"id":"wrong000001","title":"Song Name Live","channel":"Artist","duration":210}]}`
	r, calls := candidateResolverFixture(t, fixture)
	r.Match = &Track{Title: "Song Name", Artist: "Artist", Duration: 210 * time.Second}
	track, err := r.Resolve(context.Background(), "Artist Song Name")
	if err != nil || track.VideoID != "correct0001" {
		t.Fatal(track, err)
	}
	raw, _ := os.ReadFile(calls)
	text := string(raw)
	if !strings.Contains(text, "music.youtube.com/search?q=Artist+Song+Name#songs") || strings.Contains(text, "ytsearch10:Artist Song Name") || strings.Count(text, "watch?v=correct0001") != 1 {
		t.Fatal(text)
	}
	c := Candidate{ID: "abcdefghijk", Title: "Song Name", MusicSearch: true}
	rich := c
	rich.MusicSearch = false
	rich.Channel = "Artist - Topic"
	pool := deduplicateCandidates([]*Candidate{&c, &c, nil}, []*Candidate{&rich})
	if len(pool) != 1 || !pool[0].MusicSearch || pool[0].Channel != rich.Channel {
		t.Fatal(pool)
	}
	// Sparse Music entries require full metadata; a title alone is not proof.
	if b := rankSpotify(*r.Match, c, false); b.Reason == "accepted" {
		t.Fatal(b)
	}
}

func TestMusicPoolCannotBeStarvedByRichVideoResults(t *testing.T) {
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	candidates := []Candidate{{ID: "music000001", Title: "Song", MusicSearch: true}}
	for _, id := range []string{"random00001", "random00002", "random00003", "random00004", "random00005"} {
		candidates = append(candidates, Candidate{ID: id, Title: "Artist - Song", Channel: "Archive", Duration: 210})
	}
	verifications := 0
	selected, err := selectSpotify(context.Background(), track, candidates, func(c Candidate) (Candidate, error) {
		verifications++
		if c.ID == "music000001" {
			c.Channel = "Artist - Topic"
			c.Duration = 210
		}
		return c, nil
	})
	if err != nil || selected.ID != "music000001" || verifications > 4 {
		t.Fatal(selected, err, verifications)
	}
}

func TestSpotifyRejectsConcertWithoutLiveWord(t *testing.T) {
	track := Track{Title: "Hells Bells", Artist: "AC/DC", Duration: 312 * time.Second}
	c := Candidate{ID: "abcdefghijk", Title: "AC/DC - Hells Bells (Plaza De Toros De Las Ventas, July 1996)", Channel: "AC/DC", Verified: true, Duration: 315}
	if b := rankSpotify(track, c, false); b.Reason != "extra_title_content" {
		t.Fatal(b)
	}
}
func TestSpotifyRemixIdentityAndCreditsInQuery(t *testing.T) {
	track := Track{Title: "Song ft Guest - Krono Remix", Artist: "Artist", Artists: []string{"Artist", "Guest"}, Duration: 210 * time.Second}
	for _, title := range []string{"Artist Song ft Guest - Another Remix", "Artist Song Remix"} {
		c := Candidate{ID: "abcdefghijk", Title: title, Channel: "Artist", Duration: 210}
		if b := rankSpotify(track, c, false); b.Reason == "accepted" {
			t.Fatal("wrong remix accepted", b)
		}
	}
	if query := spotifyQuery(Track{Title: "Song", Artist: "Artist", Artists: []string{"Artist", "Guest"}}); !strings.Contains(query, "Guest") {
		t.Fatal(query)
	}
}
