// qa-memory adds offline diagnostics around the existing soak workload.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/qasoak"
	"whiterun/internal/storage"
)

type sink struct{}

func (sink) WriteFrame(context.Context, []byte) error { return nil }
func (sink) Speaking(context.Context, bool) error     { return nil }
func (sink) Close(context.Context)                    {}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "source" {
		return qasoak.Source(os.Args[2:])
	}
	out := flag.String("out", "", "NEW diagnostic directory; never production")
	mode := flag.String("mode", "play", "play|baseline|simulated|cycles|sqlite|defers|plain|goroutines")
	duration := flag.Duration("duration", time.Minute, "real playback/baseline duration")
	idle := flag.Duration("idle", time.Minute, "real idle after cleanup, before forced scavenging")
	count := flag.Int("count", 10000, "fixed simulated selections or player lifecycle cycles")
	noCache := flag.Bool("no-cache", false, "disable only the QA resolver cache")
	sqliteBackground := flag.Bool("sqlite-background", false, "diagnostic only: non-cancelable SQLite calls; never a production fix")
	warm := flag.Bool("warm", true, "second immediate metadata resolution")
	flag.Parse()
	if *noCache && *warm {
		return errors.New("disabled cache requires -warm=false; second resolution would not be a hit")
	}
	if *sqliteBackground && *mode != "sqlite" {
		return errors.New("-sqlite-background is restricted to the SQLite control")
	}
	if *out == "" || *duration <= 0 || *idle < 0 || *count <= 0 {
		return errors.New("invalid diagnostic arguments")
	}
	if *mode != "play" && *mode != "baseline" && *mode != "simulated" && *mode != "cycles" && *mode != "sqlite" && *mode != "defers" && *mode != "plain" && *mode != "goroutines" {
		return errors.New("unknown mode")
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime.MemProfileRate = 64 * 1024
	f, err := os.Create(filepath.Join(*out, "memory.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	var mu sync.Mutex
	start := time.Now()
	sample := func(phase string, profile bool) error {
		mu.Lock()
		defer mu.Unlock()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		all := metrics.All()
		ms := make([]metrics.Sample, len(all))
		for i, d := range all {
			ms[i].Name = d.Name
		}
		metrics.Read(ms)
		vals := map[string]any{}
		for _, s := range ms {
			switch s.Value.Kind() {
			case metrics.KindUint64:
				vals[s.Name] = s.Value.Uint64()
			case metrics.KindFloat64:
				vals[s.Name] = s.Value.Float64()
			}
		}
		r := map[string]any{"phase": phase, "elapsed_seconds": time.Since(start).Seconds(), "at": time.Now().UTC(), "memstats": m, "runtime_metrics": vals, "goroutines": runtime.NumGoroutine()}
		for _, name := range []string{"status", "smaps_rollup"} {
			b, e := os.ReadFile("/proc/self/" + name)
			if e != nil {
				return e
			}
			r[name] = string(b)
		}
		if err := json.NewEncoder(f).Encode(r); err != nil {
			return err
		}
		if profile {
			for _, name := range []string{"heap", "allocs", "goroutine"} {
				pf, e := os.Create(filepath.Join(*out, phase+"-"+name+".pprof"))
				if e != nil {
					return e
				}
				e = pprof.Lookup(name).WriteTo(pf, 0)
				ce := pf.Close()
				if e != nil {
					return e
				}
				if ce != nil {
					return ce
				}
			}
			b, e := os.ReadFile("/proc/self/smaps")
			if e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(*out, phase+"-smaps.txt"), b, 0600); e != nil {
				return e
			}
		}
		return f.Sync()
	}
	runtime.GC()
	if err = sample("baseline", true); err != nil {
		return err
	}
	// No listener or HTTP endpoint. Diagnostic errors are propagated, never hidden.
	done := make(chan struct{})
	collectErr := make(chan error, 1)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				collectErr <- nil
				return
			case <-t.C:
				if e := sample("periodic", false); e != nil {
					collectErr <- e
					return
				}
			}
		}
	}()
	selected := 0
	switch *mode {
	case "play":
		err = qasoak.Run(ctx, qasoak.Config{Out: filepath.Join(*out, "soak"), Duration: *duration, Sample: 10 * time.Second, Audio: time.Second, Playlist: 500, Guilds: 2, Seed: 20261019, Warm: *warm, Faults: true, DisableCache: *noCache})
	case "baseline":
		err = wait(ctx, *duration)
	case "goroutines":
		for i := 0; i < *count; i++ {
			done := make(chan struct{})
			go func() { defer close(done); deferredStep() }()
			<-done
		}
		selected = *count
	case "sqlite":
		storeCtx := ctx
		if *sqliteBackground {
			storeCtx = context.Background()
		}
		selected, err = sqliteControl(storeCtx, filepath.Join(*out, "qa-only.db"), *count)
	case "defers", "plain":
		ready, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(ended)
			for i := 0; i < *count; i++ {
				if *mode == "defers" {
					deferredStep()
				} else {
					plainStep()
				}
			}
			close(ready)
			<-release
		}()
		<-ready
		runtime.GC()
		err = sample("worker_alive_gc", true)
		close(release)
		<-ended
		selected = *count
	case "simulated", "cycles":
		selected, err = simulate(ctx, *count, *mode == "cycles")
	}
	close(done)
	err = errors.Join(err, <-collectErr)
	runtime.GC()
	err = errors.Join(err, sample("cleanup_gc", true))
	err = errors.Join(err, wait(ctx, *idle))
	runtime.GC()
	err = errors.Join(err, sample("idle_gc", true))
	debug.FreeOSMemory()
	err = errors.Join(err, sample("forced_scavenge", true))
	manifest := map[string]any{"mode": *mode, "requested_seconds": duration.Seconds(), "idle_seconds": idle.Seconds(), "real_elapsed_seconds": time.Since(start).Seconds(), "count": *count, "selected": selected, "no_cache": *noCache, "sqlite_background": *sqliteBackground, "warm": *warm, "go_version": runtime.Version(), "mem_profile_rate": runtime.MemProfileRate, "error": fmt.Sprint(err)}
	b, e := json.MarshalIndent(manifest, "", "  ")
	err = errors.Join(err, e)
	if e == nil {
		err = errors.Join(err, os.WriteFile(filepath.Join(*out, "diagnostic.json"), b, 0600))
	}
	return err
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func simulate(ctx context.Context, n int, cycles bool) (int, error) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	completed := make(chan string, 1)
	newPlayer := func() *player.GuildPlayer {
		return player.New(ctx, log, func(ctx context.Context, tr media.Track, v player.Voice) error {
			if e := v.WriteFrame(ctx, []byte{1}); e != nil {
				return e
			}
			select {
			case completed <- tr.VideoID:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	p := newPlayer()
	defer func() { p.Close(context.Background()) }()
	if e := p.Attach(sink{}); e != nil {
		return 0, e
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%011d", i)
		if e := p.Enqueue(media.Track{Title: id, VideoID: id}, p.Ticket()); e != nil {
			return i, e
		}
		select {
		case got := <-completed:
			if got != id {
				return i, errors.New("simulated order/identity violated")
			}
		case <-ctx.Done():
			return i, ctx.Err()
		}
		if cycles {
			p.Close(context.Background())
			p = newPlayer()
			if e := p.Attach(sink{}); e != nil {
				return i, e
			}
		}
	}
	return n, nil
}

//go:noinline
func deferredStep() { defer plainStep() }

//go:noinline
func plainStep() {}

// sqliteControl holds a constant logical queue across writes; no network,
// subprocess, resolver cache, player, or growing set of track identities.
func sqliteControl(ctx context.Context, path string, n int) (int, error) {
	db, err := storage.Open(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	state := player.DurableState{Queue: make([]media.Track, 500)}
	for i := range state.Queue {
		state.Queue[i] = media.Track{Title: fmt.Sprint(i), VideoID: fmt.Sprintf("%011d", i)}
	}
	for i := 0; i < n; i++ {
		if err = db.SaveGuild(ctx, "qa012", state); err != nil {
			return i, err
		}
	}
	saved, err := db.LoadGuild(ctx, "qa012")
	if err != nil {
		return n, err
	}
	if len(saved.Queue) != 500 {
		return n, errors.New("sqlite control lost queue entries")
	}
	for i, tr := range saved.Queue {
		if tr.VideoID != state.Queue[i].VideoID {
			return n, errors.New("sqlite control changed order/identity")
		}
	}
	return n, db.Close()
}
