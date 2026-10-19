//go:build qa008_knownissues

package player

import (
	"bytes"
	"testing"
)

// Intentionally red reproduction, not a passing assertion that blesses the bug.
func TestKnownIssuesEmptyOpusPacket(t *testing.T) {
	stream := append(page(2, []byte{8}, []byte("OpusHead")), page(0, []byte{0}, nil)...)
	delivered := 0
	err := oggPackets(bytes.NewReader(stream), func(packet []byte) error { delivered++; return nil })
	if err == nil || delivered != 0 {
		t.Fatalf("QA008-D02: empty audio packet delivered as Opus: delivered=%d err=%v", delivered, err)
	}
}
