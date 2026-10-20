package player

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"whiterun/internal/media"
)

// QA008-D02 must reject empty audio before a consumer can announce playback.
func TestDefectEmptyOpusPacket(t *testing.T) {
	stream := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{0}, nil)...)
	delivered := 0
	err := oggPackets(bytes.NewReader(stream), func(packet []byte) error { delivered++; return nil })
	if err == nil || delivered != 0 {
		t.Fatalf("QA008-D02: empty audio packet delivered as Opus: delivered=%d err=%v", delivered, err)
	}
}

func TestDefectEmptyOpusNeverStartsStream(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			data := page(2, []byte{8}, []byte("OpusHead"))
			if partial {
				data = append(data, page(0, []byte{1}, []byte{1})...)
			}
			data = append(data, page(0, []byte{0}, nil)...)
			file := filepath.Join(t.TempDir(), "empty.ogg")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			yt := executable(t, "yt-dlp", "cat '"+file+"'")
			ff := executable(t, "ffmpeg", "cat")
			v := &recordingVoice{}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := (Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, media.Track{}, v)
			want := 0
			if partial {
				want = 1
			}
			if err == nil || v.frames != want || v.speaking {
				t.Fatalf("empty audio delivered or retained speaking: frames=%d err=%v", v.frames, err)
			}
		})
	}
}
