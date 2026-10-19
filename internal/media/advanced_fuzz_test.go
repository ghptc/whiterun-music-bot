package media

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func FuzzAdvancedMediaURLs(f *testing.F) {
	for _, s := range []string{
		"https://youtu.be/abcdefghijk", "https://youtube.com/watch?v=abcdefghijk&list=PLx",
		"https://open.spotify.com/track/0123456789012345678901",
		"https://open.spotify.com@127.0.0.1/track/0123456789012345678901",
		"https://youtube.com.evil/watch?v=abcdefghijk", "file:///etc/passwd",
		"https://open.spotify.com/%zz", "\x00🎵", "https://youtube.com:443/watch?v=abcdefghijk",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		canonical, recognized, err := YouTubeURL(raw)
		if err == nil && recognized {
			u, e := url.Parse(canonical)
			if e != nil || u.Scheme != "https" || u.Host != "www.youtube.com" || u.User != nil || u.Path != "/watch" || len(u.Query()) != 1 || u.Fragment != "" {
				t.Fatalf("unsafe canonical URL %q", canonical)
			}
			id := u.Query().Get("v")
			if len(id) != 11 || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
				t.Fatal("invalid video ID", id)
			}
			again, ok, e := YouTubeURL(canonical)
			if e != nil || !ok || again != canonical {
				t.Fatal("canonical URL is not idempotent")
			}
		}
		source, kind, id, e := CollectionURL(raw)
		if e == nil && source == "spotify" {
			if kind != "track" && kind != "album" && kind != "playlist" {
				t.Fatal("invalid Spotify resource", kind)
			}
			if len(id) != 22 || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
				t.Fatal("invalid Spotify ID", id)
			}
			a, b, c, e := CollectionURL("https://open.spotify.com/" + kind + "/" + id)
			if e != nil || a != source || b != kind || c != id {
				t.Fatal("Spotify canonical identity changed")
			}
		}
		if e == nil && source == "youtube" {
			if kind != "playlist" || id == "" || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
				t.Fatal("invalid collection", kind, id)
			}
		}
		safe := SafeArtworkURL(raw)
		if safe != "" {
			u, err := url.Parse(safe)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
				t.Fatalf("unsafe artwork %q", safe)
			}
		}
	})
}
func FuzzAdvancedRecordingMetadata(f *testing.F) {
	for _, title := range []string{"The Long Road Through Winter Mountains", "Déjà Vu", "東京の夜", "Song \x00", "🎵 🐉", "Artist - Song (Live)"} {
		f.Add(title, "Artist", byte(0))
	}
	f.Fuzz(func(t *testing.T, title, artist string, variant byte) {
		if len(title)+len(artist) > 2048 {
			t.Skip()
		}
		tr := Track{Title: title, Artist: artist, Artists: []string{artist}, Duration: 210 * time.Second, ISRC: "USAAA1200001"}
		c := Candidate{ID: "abcdefghijk", Title: title, Track: title, Artists: []string{artist}, Channel: artist, Verified: true, Duration: 210, ISRC: "USAAA1200002"}
		if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchRejected {
			t.Fatalf("Unicode metadata bypassed ISRC contradiction: %+v", d)
		}
		c.ISRC = tr.ISRC
		c.Explicit = new(false)
		tr.Explicit = new(true)
		if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status != MatchRejected {
			t.Fatalf("explicit contradiction accepted: %+v", d)
		}
		tr.Explicit = nil
		c.Explicit = nil
		versions := []string{"Live", "Remix", "Karaoke", "Instrumental", "Cover", "Acoustic"}
		// A requested version and a different version must never be confirmed,
		// even if arbitrary titles/credits already contain version tokens.
		tr.Title = "The Long Road Through Winter Mountains (" + versions[int(variant)%len(versions)] + ")"
		c.Title = "The Long Road Through Winter Mountains (" + versions[(int(variant)+1)%len(versions)] + ")"
		c.Track = c.Title
		if d := CompareSpotify(tr, c, DefaultMatchPolicy()); d.Status == MatchConfirmed {
			t.Fatalf("recording variant replaced: %+v", d)
		}
	})
}
func FuzzAdvancedDiagnosticBudget(f *testing.F) {
	f.Add([]byte("secret https://example.org/private?token=x"), uint16(32), false)
	f.Add([]byte("\xff\x00🎵"), uint16(1), true)
	f.Fuzz(func(t *testing.T, data []byte, limit uint16, tail bool) {
		if len(data) > 8192 {
			t.Skip()
		}
		budget := int(limit%1024) + 1
		b := &LimitedBuffer{Limit: budget, Tail: tail}
		mid := len(data) / 2
		n, e := b.Write(data[:mid])
		if e != nil || n != mid {
			t.Fatal("writer contract")
		}
		n, e = b.Write(data[mid:])
		if e != nil || n != len(data)-mid {
			t.Fatal("writer contract")
		}
		want := data
		if len(data) > budget {
			if tail {
				want = data[len(data)-budget:]
			} else {
				want = data[:budget]
			}
		}
		if string(b.Data) != string(want) || b.Truncated != (len(data) > budget) {
			t.Fatal("diagnostic buffer changed ownership or budget")
		}
	})
}
