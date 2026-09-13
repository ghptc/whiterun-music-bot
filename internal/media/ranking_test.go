package media

import "testing"

func song(title, channel string) Candidate {
	return Candidate{ID: "abcdefghijk", Title: title, Channel: channel, Duration: 240, Categories: []string{"Music"}}
}
func TestCanonicalSelection(t *testing.T) {
	official := song("Arctic Monkeys - 505 (Official Audio)", "Arctic Monkeys")
	candidates := []Candidate{
		song("Arctic Monkeys 505 reaction", "React channel"),
		song("Arctic Monkeys 505 cover", "Singer"),
		song("Arctic Monkeys 505 slowed + reverb", "Edits"),
		song("A completely different song", "Other artist"),
		official,
	}
	best, ok := Select("Arctic Monkeys 505", candidates)
	if !ok || best.Title != official.Title {
		t.Fatalf("selected %#v, ok=%v", best, ok)
	}
}
func TestRequestedVariants(t *testing.T) {
	for _, tt := range []struct{ query, normal, version string }{
		{"Arctic Monkeys 505 live", "Arctic Monkeys - 505 (Official Audio)", "Arctic Monkeys - 505 live Reading 2009"},
		{"Arctic Monkeys 505 live Reading 2009", "Arctic Monkeys - 505 (Official Audio)", "Arctic Monkeys - 505 live Reading 2009"},
		{"Alice in Chains Nutshell unplugged", "Alice in Chains - Nutshell (Official Audio)", "Alice in Chains - Nutshell (MTV Unplugged live)"},
		{"Arctic Monkeys 505 cover", "Arctic Monkeys - 505 (Official Audio)", "Arctic Monkeys - 505 cover"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			normal := song(tt.normal, "Official artist")
			version := song(tt.version, "Concert archive")
			best, ok := Select(tt.query, []Candidate{normal, version})
			if !ok || best.Title != version.Title {
				t.Fatalf("selected %q", best.Title)
			}
		})
	}
}
func TestCanonicalSourceOrder(t *testing.T) {
	oac := song("Arctic Monkeys - 505", "Arctic Monkeys")
	oac.Artist = "Arctic Monkeys"
	oac.Verified = true
	official := song("Arctic Monkeys - 505 Official Audio", "Domino")
	topic := song("505", "Arctic Monkeys - Topic")
	vevo := song("Arctic Monkeys - 505", "ArcticMonkeysVEVO")
	verified := song("Arctic Monkeys - 505", "Arctic Monkeys Records")
	verified.Verified = true
	generic := song("Arctic Monkeys - 505", "Music archive")
	ordered := []Candidate{oac, official, topic, vevo, verified, generic}
	for i := 0; i < len(ordered)-1; i++ {
		a, ok := Score("Arctic Monkeys 505", ordered[i])
		b, _ := Score("Arctic Monkeys 505", ordered[i+1])
		if !ok || a <= b {
			t.Fatalf("tier %d: %d should exceed %d", i, a, b)
		}
	}
}
func TestNonMusicRejection(t *testing.T) {
	for _, term := range []string{"podcast", "interview", "reaction", "review", "tutorial", "documentary", "gameplay", "news", "shorts"} {
		c := song("Arctic Monkeys 505 "+term, "Channel")
		if _, ok := Select("Arctic Monkeys 505", []Candidate{c}); ok {
			t.Errorf("accepted %s", term)
		}
	}
	for _, change := range []func(*Candidate){
		func(c *Candidate) { c.Duration = 20 }, func(c *Candidate) { c.Duration = 3600 }, func(c *Candidate) { c.Live = true },
		func(c *Candidate) { c.Categories = nil }, func(c *Candidate) { c.LiveStatus = "is_upcoming" },
	} {
		c := song("Arctic Monkeys 505", "Channel")
		change(&c)
		if IsMusic(c) {
			t.Errorf("accepted %#v", c)
		}
	}
	if _, ok := Select("Arctic Monkeys 505", []Candidate{song("Other artist - Other song (Official Audio)", "Other artist")}); ok {
		t.Fatal("accepted unrelated song")
	}
	if _, ok := Select("Arctic Monkeys 505", nil); ok {
		t.Fatal("accepted empty results")
	}
}
func TestURLValidation(t *testing.T) {
	for _, s := range []string{"https://www.youtube.com/watch?v=abcdefghijk&list=ignored", "https://youtu.be/abcdefghijk?t=30", "music.youtube.com/watch?v=abcdefghijk", "https://youtube.com/embed/abcdefghijk"} {
		u, ok, err := YouTubeURL(s)
		if err != nil || !ok || u != "https://www.youtube.com/watch?v=abcdefghijk" {
			t.Errorf("%s: %q %v %v", s, u, ok, err)
		}
	}
	for _, s := range []string{"https://youtube.com.evil.test/watch?v=abcdefghijk", "file:///etc/passwd", "https://example.com/a", "https://youtube.com/playlist?list=x", "https://youtube.com/shorts/abcdefghijk", "https://youtube.com/watch?v=bad", "https://user@youtube.com/watch?v=abcdefghijk", "https://youtube.com:8080/watch?v=abcdefghijk"} {
		if _, _, err := YouTubeURL(s); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	if _, ok, err := YouTubeURL("505 Arctic Monkeys"); ok || err != nil {
		t.Fatal("query treated as URL")
	}
}
