package player

import (
	"bytes"
	"errors"
	"io"
)

// oggPackets reconstructs packets across Ogg pages. FFmpeg emits 20ms Opus
// packets; OpusHead/OpusTags are container metadata, not Discord audio frames.
func oggPackets(r io.Reader, send func([]byte) error) error {
	var packet []byte
	seenHead := false
	for {
		var header [27]byte
		_, err := io.ReadFull(r, header[:])
		if err == io.EOF {
			if len(packet) != 0 {
				return io.ErrUnexpectedEOF
			}
			if !seenHead {
				return errors.New("missing Opus header")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if string(header[:4]) != "OggS" || header[4] != 0 {
			return errors.New("invalid Ogg stream")
		}
		if (header[5]&1 != 0) != (len(packet) > 0) {
			return errors.New("invalid Ogg continuation")
		}
		sizes := make([]byte, int(header[26]))
		if _, err = io.ReadFull(r, sizes); err != nil {
			return err
		}
		for _, size := range sizes {
			start := len(packet)
			if start+int(size) > 65536 {
				return errors.New("oversized Opus packet")
			}
			packet = append(packet, make([]byte, int(size))...)
			if _, err = io.ReadFull(r, packet[start:]); err != nil {
				return err
			}
			if size == 255 {
				continue
			}
			if bytes.HasPrefix(packet, []byte("OpusHead")) {
				seenHead = true
			} else if !bytes.HasPrefix(packet, []byte("OpusTags")) {
				if !seenHead {
					return errors.New("audio before Opus header")
				}
				if err = send(packet); err != nil {
					return err
				}
			}
			packet = nil
		}
	}
}
