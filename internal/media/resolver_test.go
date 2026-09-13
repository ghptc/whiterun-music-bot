package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resolverFixture(t *testing.T, output string) (Resolver, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\ncat <<'JSON'\n" + output + "\nJSON\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return Resolver{Binary: binary}, args
}
func TestResolverSearchesMultipleCandidates(t *testing.T) {
	r, args := resolverFixture(t, `{"entries":[null,{"id":"abcdefghijk","title":"Arctic Monkeys - 505 Official Audio","channel":"Arctic Monkeys","duration":240,"categories":["Music"]}]}`)
	track, err := r.Resolve(context.Background(), "Arctic Monkeys 505")
	if err != nil || track.URL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("%#v %v", track, err)
	}
	b, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--flat-playlist") || strings.Contains(string(b), "youtube:player_client=") || !strings.Contains(string(b), "ytsearch10:Arctic Monkeys 505") || !strings.Contains(string(b), "--ignore-config") {
		t.Fatalf("args: %s", b)
	}
}
func TestResolverDirectVideoIsFiltered(t *testing.T) {
	for _, tt := range []struct {
		title    string
		rejected bool
	}{{"Arctic Monkeys 505 live", false}, {"Arctic Monkeys interview", true}} {
		r, args := resolverFixture(t, `{"id":"abcdefghijk","title":"`+tt.title+`","duration":240,"categories":["Music"]}`)
		_, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk?list=ignored")
		if tt.rejected && !errors.Is(err, ErrNoSong) || !tt.rejected && err != nil {
			t.Fatalf("%s: %v", tt.title, err)
		}
		b, _ := os.ReadFile(args)
		if strings.Contains(string(b), "ytsearch") || !strings.Contains(string(b), "https://www.youtube.com/watch?v=abcdefghijk") {
			t.Fatalf("args %s", b)
		}
	}
}
func TestResolverInvalidAndEmptyMetadata(t *testing.T) {
	for _, data := range []string{`invalid json`, `{"entries":[]}`, `{"entries":[null]}`} {
		r, _ := resolverFixture(t, data)
		if _, err := r.Resolve(context.Background(), "Arctic Monkeys 505"); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestResolverFlatSearchExtractsOnlySelectedVideo(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "yt-dlp")
	args := filepath.Join(dir, "calls")
	script := `#!/bin/sh
for arg do target="$arg"; done
printf '%s\n' "$*" >> '` + args + `'
case "$target" in
ytsearch10:*)
cat <<'JSON'
{"entries":[{"_type":"url","id":"abcdefghijk","title":"Arctic Monkeys 505 live","channel":"Archive","duration":240},{"_type":"url","id":"lmnopqrstuv","title":"Arctic Monkeys 505 live interview","channel":"Archive","duration":240},{"_type":"url","id":"12345678901","title":"Arctic Monkeys 505 Official Audio","channel":"Artist","duration":240}]}
JSON
;;
*)
cat <<'JSON'
{"id":"abcdefghijk","title":"Arctic Monkeys 505 live","channel":"Archive","duration":240,"categories":["Music"]}
JSON
;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := Resolver{Binary: binary}
	track, err := r.Resolve(context.Background(), "Arctic Monkeys 505 live")
	if err != nil || track.VideoID != "abcdefghijk" || track.Channel != "Archive" {
		t.Fatalf("%+v %v", track, err)
	}
	b, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) != 2 {
		t.Fatalf("calls: %s", b)
	}
	if !strings.Contains(calls[0], "--flat-playlist") || !strings.Contains(calls[0], "ytsearch10:") {
		t.Fatalf("search: %s", calls[0])
	}
	if strings.Contains(calls[1], "--flat-playlist") || !strings.HasSuffix(calls[1], "https://www.youtube.com/watch?v=abcdefghijk") {
		t.Fatalf("verification: %s", calls[1])
	}
}
