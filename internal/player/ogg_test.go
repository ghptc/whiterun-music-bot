package player

import (
	"bytes"
	"testing"
)

func page(flags byte, sizes []byte, payload []byte) []byte {
	h := make([]byte, 27)
	copy(h, "OggS")
	h[5] = flags
	h[26] = byte(len(sizes))
	return append(append(h, sizes...), payload...)
}
func TestOggPackets(t *testing.T) {
	stream := page(2, []byte{8}, []byte("OpusHead"))
	stream = append(stream, page(0, []byte{8}, []byte("OpusTags"))...)
	stream = append(stream, page(0, []byte{255}, bytes.Repeat([]byte{7}, 255))...)
	stream = append(stream, page(1, []byte{2, 3}, []byte{7, 7, 1, 2, 3})...)
	var packets [][]byte
	if err := oggPackets(bytes.NewReader(stream), func(p []byte) error { packets = append(packets, bytes.Clone(p)); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 2 || len(packets[0]) != 257 || !bytes.Equal(packets[1], []byte{1, 2, 3}) {
		t.Fatalf("bad packets %v", packets)
	}
}
func TestOggRejectsInvalidAndTruncated(t *testing.T) {
	for _, stream := range [][]byte{nil, []byte("not ogg"), page(0, []byte{10}, []byte("short")), page(1, []byte{8}, []byte("OpusHead"))} {
		if err := oggPackets(bytes.NewReader(stream), func([]byte) error { return nil }); err == nil {
			t.Fatal("accepted invalid stream")
		}
	}
}
