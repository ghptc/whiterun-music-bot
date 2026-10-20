package media

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Opt-in paired probe: no Discord connection, no hard external network budget.
// The synchronous path remains available to measure the exact eager baseline.
func TestPERF018ProviderMetadata(t *testing.T) {
	if os.Getenv("BARD_PERF018_PROVIDER") != "1" {
		t.Skip("explicit real-provider comparison")
	}
	s := &Spotify{ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN")}
	for _, fast := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		ctx = WithTiming(ctx, NewTiming(slog.Default()))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var cpuBefore, cpuAfter syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
		start := time.Now()
		var c Collection
		var err error
		if fast {
			c, err = s.CollectionForPlayback(ctx, "playlist", "0OdkLDK5wUdzAd2OT03hQz")
		} else {
			c, err = s.Collection(ctx, "playlist", "0OdkLDK5wUdzAd2OT03hQz")
		}
		elapsed := time.Since(start)
		cancel()
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
		runtime.ReadMemStats(&after)
		pending := 0
		for _, tr := range c.Tracks {
			if tr.MetadataPending {
				pending++
			}
		}
		cpu := func(r syscall.Rusage) int64 { return r.Utime.Sec*1e6 + r.Utime.Usec + r.Stime.Sec*1e6 + r.Stime.Usec }
		row := map[string]any{"environment": "real-spotify-metadata-no-voice", "deferred": fast, "elapsed_ms": float64(elapsed) / float64(time.Millisecond), "cpu_self_ms": float64(cpu(cpuAfter)-cpu(cpuBefore)) / 1000, "alloc_bytes": after.TotalAlloc - before.TotalAlloc, "heap_end_bytes": after.HeapAlloc, "tracks": len(c.Tracks), "pending": pending, "success": err == nil}
		if err != nil {
			row["error"] = SafeDiagnostic([]byte(err.Error()))
		}
		raw, _ := json.Marshal(row)
		t.Log("PERF018_METADATA_JSON " + string(raw))
	}
}
