package player

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"whiterun/internal/media"
)

// Open the descriptor once under a confined root. Never pass a pathname to
// FFmpeg: rename/unlink after opening cannot redirect the decoder to another file.
// Initially only WAV and FLAC are allowed; playlists and network protocols aren't.
func openLocalAudio(directory, id string) (*os.File, string, error) {
	fail := func() (*os.File, string, error) {
		return nil, "", &StreamError{"local_audio_open", errors.New("local audio unavailable or not permitted")}
	}
	if !filepath.IsAbs(directory) || !media.ValidLocalAudioID(id) {
		return fail()
	}
	format := ""
	switch filepath.Ext(id) {
	case ".wav":
		format = "wav"
	case ".flac":
		format = "flac"
	default:
		return fail()
	}
	root, err := os.OpenFile(directory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fail()
	}
	defer root.Close()
	rootInfo, err := root.Stat()
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0022 != 0 {
		return fail()
	}
	// The ID cannot contain separators. openat pins the directory and Linux
	// O_NOFOLLOW rejects the final symlink atomically (including in-root links).
	fd, err := syscall.Openat(int(root.Fd()), id, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fail()
	}
	f := os.NewFile(uintptr(fd), "local-audio")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0444 == 0 {
		f.Close()
		return fail()
	}
	header := make([]byte, 12)
	_, err = io.ReadFull(f, header)
	valid := err == nil && ((format == "wav" && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WAVE"))) || (format == "flac" && bytes.Equal(header[:4], []byte("fLaC"))))
	if !valid {
		f.Close()
		return fail()
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return fail()
	}
	return f, format, nil
}
