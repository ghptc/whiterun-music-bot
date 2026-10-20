package qasoak

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

// Source is the controlled yt-dlp stand-in. It never opens a network socket.
func Source(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("missing local fixture")
	}
	target, err := url.Parse(args[len(args)-1])
	if err != nil {
		return err
	}
	id := target.Query().Get("v")
	for _, arg := range args {
		if arg == "--dump-single-json" {
			f, err := os.OpenFile(args[0]+".metadata-calls", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.WriteString(id + "\n")
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"id": id, "title": "local recording", "duration": 1})
		}
	}
	if target.Query().Get("qa_fault") == "source" {
		return fmt.Errorf("QA controlled source unavailable")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(os.Stdout, f)
	return err
}
func wave(path string, d time.Duration) error {
	n := int(d.Seconds()*48000) * 4
	b := make([]byte, 44+n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 2)
	binary.LittleEndian.PutUint32(b[24:], 48000)
	binary.LittleEndian.PutUint32(b[28:], 192000)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(n))
	// A low-amplitude deterministic square wave; FFmpeg performs actual Opus encoding.
	for i := 0; i < n/2; i++ {
		v := int16(1000)
		if (i/48)%2 == 0 {
			v = -1000
		}
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(v))
	}
	return os.WriteFile(path, b, 0600)
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
