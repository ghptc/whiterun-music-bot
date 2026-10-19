package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCanceledSpotifyMetadataProcessIsReaped(t *testing.T) {
	dir := t.TempDir()
	binary, pidfile := filepath.Join(dir, "yt-dlp"), filepath.Join(dir, "pid")
	// exec preserves the PID, so the process blocked on metadata I/O is also the
	// process which os/exec must kill and reap before returning cancellation.
	script := "#!/bin/sh\necho $$ > '" + pidfile + "'\nexec sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	track := Track{Title: "Song", Artist: "Artist", Duration: 210 * time.Second}
	resolver := Resolver{Binary: binary, Match: &track}
	done := make(chan error, 1)
	go func() { _, err := resolver.Resolve(ctx, "query"); done <- err }()
	var pid int
	deadline := time.After(2 * time.Second)
	for pid == 0 {
		raw, _ := os.ReadFile(pidfile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("metadata process did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("metadata process/goroutine leaked")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("metadata process %d not reaped: %v", pid, err)
	}
}
