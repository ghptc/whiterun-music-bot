package qasoak

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux procfs counters refer only to the harness process, not the host.
func resources(phase string, start time.Time) map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out := map[string]any{"phase": phase, "at": time.Now().UTC(), "elapsed_seconds": time.Since(start).Seconds(), "heap_bytes": m.HeapAlloc, "heap_objects": m.HeapObjects, "total_alloc_bytes": m.TotalAlloc, "goroutines": runtime.NumGoroutine(), "cache_memory_bytes": nil}
	if f, err := os.Open("/proc/self/status"); err == nil {
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			p := strings.Fields(scan.Text())
			if len(p) < 2 {
				continue
			}
			n, _ := strconv.ParseInt(p[1], 10, 64)
			switch p[0] {
			case "VmRSS:":
				out["rss_bytes"] = n * 1024
			case "Threads:":
				out["threads"] = n
			}
		}
		f.Close()
	}
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		out["file_descriptors"] = len(fds)
	}
	if tasks, err := os.ReadDir("/proc/self/task"); err == nil {
		children := map[string]bool{}
		for _, task := range tasks {
			b, _ := os.ReadFile("/proc/self/task/" + task.Name() + "/children")
			for _, pid := range strings.Fields(string(b)) {
				children[pid] = true
			}
		}
		out["child_processes"] = len(children)
		out["child_pids"] = children
	}
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) == nil {
		out["cpu_seconds"] = float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	}
	if syscall.Getrusage(syscall.RUSAGE_CHILDREN, &usage) == nil {
		out["reaped_children_cpu_seconds"] = float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	}
	return out
}
