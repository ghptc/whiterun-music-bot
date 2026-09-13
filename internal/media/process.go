package media

import (
	"context"
	"os"
	"os/exec"
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
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := b.Limit - len(b.Data)
	if len(p) > left {
		p = p[:left]
		b.Truncated = true
	}
	b.Data = append(b.Data, p...)
	return n, nil
}
