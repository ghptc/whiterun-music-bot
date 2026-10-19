package player

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestQualityOggTruncation(t *testing.T) {
	head := page(2, []byte{8}, []byte("OpusHead"))
	audio := page(0, []byte{4}, []byte{1, 2, 3, 4})
	stream := append(bytes.Clone(head), audio...)
	// A complete header-only stream is a valid parser boundary. Empty audio is
	// rejected at Streamer level, not by the container parser.
	for cut := 0; cut < len(stream); cut++ {
		if cut == len(head) {
			continue
		}
		t.Run(fmt.Sprintf("cut=%d", cut), func(t *testing.T) {
			calls := 0
			err := oggPackets(bytes.NewReader(stream[:cut]), func([]byte) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("truncated stream accepted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestQualityOggPacketBoundaries(t *testing.T) {
	for _, length := range []int{1, 2, 254, 255, 256, 510, 511, 1024, 65535} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			payload := bytes.Repeat([]byte{0x71}, length)
			stream := page(2, []byte{8}, []byte("OpusHead"))
			remaining := length
			offset := 0
			flags := byte(0)
			// Each 255-byte segment crosses a page, including the terminal zero
			// segment required for exact multiples of 255.
			for remaining >= 255 {
				stream = append(stream, page(flags, []byte{255}, payload[offset:offset+255])...)
				remaining -= 255
				offset += 255
				flags = 1
			}
			stream = append(stream, page(flags, []byte{byte(remaining)}, payload[offset:])...)
			stream = append(stream, page(0, []byte{1}, []byte{0x72})...)
			var packets [][]byte
			if err := oggPackets(bytes.NewReader(stream), func(b []byte) error { packets = append(packets, bytes.Clone(b)); return nil }); err != nil {
				t.Fatal(err)
			}
			if len(packets) != 2 || !bytes.Equal(packets[0], payload) || !bytes.Equal(packets[1], []byte{0x72}) {
				t.Fatal("packet reconstruction changed audio")
			}
		})
	}
}
func TestQualityOggConsumerFailure(t *testing.T) {
	sentinel := errors.New("voice disconnected")
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			stream := page(2, []byte{8}, []byte("OpusHead"))
			for range 4 {
				stream = append(stream, page(0, []byte{1}, []byte{1})...)
			}
			calls := 0
			err := oggPackets(bytes.NewReader(stream), func([]byte) error {
				calls++
				if calls == failAt {
					return sentinel
				}
				return nil
			})
			if !errors.Is(err, sentinel) || calls != failAt {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
