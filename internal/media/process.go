package media

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Command kills the whole process group, including yt-dlp's JS helpers.
// The application targets Linux containers.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// LimitedBuffer consumes output without allowing subprocess diagnostics/JSON to
// grow memory indefinitely. Each instance belongs to one subprocess writer.
type LimitedBuffer struct {
	Data      []byte
	Limit     int
	Truncated bool
	Tail      bool
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Tail {
		b.Data = append(b.Data, p...)
		if len(b.Data) > b.Limit {
			b.Data = append([]byte(nil), b.Data[len(b.Data)-b.Limit:]...)
			b.Truncated = true
		}
		return n, nil
	}
	left := b.Limit - len(b.Data)
	if len(p) > left {
		p = p[:left]
		b.Truncated = true
	}
	b.Data = append(b.Data, p...)
	return n, nil
}

var diagnosticURL = regexp.MustCompile(`https?://[^\s]+`)

func SafeDiagnostic(data []byte) string {
	return strings.TrimSpace(diagnosticURL.ReplaceAllString(string(data), "[url]"))
}
