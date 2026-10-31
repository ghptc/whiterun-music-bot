package media

import (
	"testing"
	"time"
)

func TestLocalTrackDescriptor(t *testing.T) {
	for _, id := range []string{"../a.wav", "/etc/passwd", "a/b.wav", "a;touch.wav", "$(id).wav", "-a.wav", ""} {
		if _, err := LocalTrack(id, time.Second); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	tr, err := LocalTrack("validation.flac", 30*time.Second)
	if err != nil || tr.RequestedSource != "local" || tr.URL != "validation.flac" || tr.VerifyBeforePlay {
		t.Fatal(tr, err)
	}
	if _, err := LocalTrack("validation.flac", 0); err == nil {
		t.Fatal("zero duration")
	}
}
