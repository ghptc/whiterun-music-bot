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
	if !strings.Contains(string(b), "ytsearch10:Arctic Monkeys 505") || !strings.Contains(string(b), "--ignore-config") {
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
