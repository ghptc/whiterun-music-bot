package player

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func FuzzAdvancedOgg(f *testing.F) {
	head := page(2, []byte{8}, []byte("OpusHead"))
	f.Add([]byte{})
	f.Add(head)
	f.Add(append(bytes.Clone(head), page(0, []byte{1}, []byte{1})...))
	f.Add(page(1, []byte{255}, bytes.Repeat([]byte{1}, 255)))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 131072 {
			t.Skip()
		}
		fail := errors.New("consumer canceled")
		count := 0
		err := oggPackets(bytes.NewReader(data), func(packet []byte) error {
			count++
			if len(packet) > 65536 {
				t.Fatal("unbounded packet")
			}
			return fail
		})
		if count > 1 || (count == 1 && !errors.Is(err, fail)) {
			t.Fatalf("consumer cancellation ignored: calls=%d err=%v", count, err)
		}
	})
}
func FuzzAdvancedTimestamp(f *testing.F) {
	for _, s := range []string{"00:00", "24:00:00", "1440:00", "23:59:59", "1:60", "-1:00", "١:٣٠", "\x00:00", "999999:59"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1024 {
			t.Skip()
		}
		got, err := ParseTimestamp(raw)
		if err != nil {
			return
		}
		if got < 0 || got > 24*time.Hour || got%time.Second != 0 {
			t.Fatal("timestamp outside command resource limit", got)
		}
		// Independent positional arithmetic verifies every accepted byte.
		parts := strings.Split(raw, ":")
		if len(parts) != 2 && len(parts) != 3 {
			t.Fatal("accepted invalid arity")
		}
		seconds := int64(0)
		for i, part := range parts {
			if len(part) == 0 || len(part) > 6 {
				t.Fatal("invalid field length")
			}
			value := int64(0)
			for _, r := range part {
				if r < '0' || r > '9' {
					t.Fatal("non-ASCII timestamp accepted")
				}
				value = value*10 + int64(r-'0')
			}
			if i > 0 && value > 59 {
				t.Fatal("invalid base-60 field")
			}
			seconds = seconds*60 + value
		}
		if got != time.Duration(seconds)*time.Second {
			t.Fatal("timestamp arithmetic mismatch")
		}
		canonical := fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
		round, e := ParseTimestamp(canonical)
		if e != nil || round != got {
			t.Fatal("timestamp roundtrip")
		}
	})
}
