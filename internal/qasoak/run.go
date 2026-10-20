package qasoak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"whiterun/internal/media"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type Config struct {
	Out                     string
	Duration, Sample, Audio time.Duration
	Playlist, Guilds        int
	Seed                    uint64
	Warm, Resume, Faults    bool
}
type histogram struct {
	Count    uint64
	Sum, Max int64
	Bins     [64]uint64
}

func (h *histogram) add(d time.Duration) {
	n := max(int64(d), 0)
	h.Count++
	h.Sum += n
	h.Max = max(h.Max, n)
	h.Bins[min(bits.Len64(uint64(n)), 63)]++
}
func (h histogram) summary() map[string]any {
	r := map[string]any{"count": h.Count, "max_ms": float64(h.Max) / 1e6, "quantiles": "upper bounds from fixed power-of-two nanosecond buckets"}
	for name, q := range map[string]float64{"p50_ms": .5, "p95_ms": .95, "p99_ms": .99} {
		var seen uint64
		for i, n := range h.Bins {
			seen += n
			if h.Count > 0 && float64(seen) >= float64(h.Count)*q {
				r[name] = float64(uint64(1)<<i) / 1e6
				break
			}
		}
	}
	if h.Count > 0 {
		r["mean_ms"] = float64(h.Sum) / float64(h.Count) / 1e6
	}
	return r
}

type journal struct {
	mu  sync.Mutex
	f   *os.File
	err error
}

func (j *journal) write(v any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err == nil {
		j.err = json.NewEncoder(j.f).Encode(v)
	}
}

type guildRun struct {
	mu                                             sync.Mutex
	id                                             string
	p                                              *player.GuildPlayer
	expected                                       []media.Track
	accepted, selected, announced, frames, batches int
	terminal                                       map[string]int
	metrics                                        map[string]*histogram
	lastEnd                                        time.Time
	done                                           chan struct{}
	err                                            error
	active                                         *attempt
}
type attempt struct {
	announced bool
	writes    atomic.Int64
	frames    atomic.Int64
	fault     string
	cancel    context.CancelFunc
	selected  time.Time
	first     time.Time
}
type voice struct{ g *guildRun }

func (v *voice) WriteFrame(ctx context.Context, _ []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.g.mu.Lock()
	a := v.g.active
	v.g.mu.Unlock()
	n := a.writes.Add(1)
	if a.fault == "transport" && n == 4 {
		return errors.New("QA controlled audio transport disconnected")
	}
	if a.fault == "cancel" && n == 4 {
		a.cancel()
		return context.Canceled
	}
	a.frames.Add(1)
	if n == 1 {
		v.g.mu.Lock()
		a.first = time.Now()
		v.g.observe("delivery_first_frame", a.first.Sub(a.selected))
		v.g.mu.Unlock()
	}
	return nil
}
func (*voice) Speaking(context.Context, bool) error { return nil }
func (*voice) Close(context.Context)                {}
func (g *guildRun) observe(name string, d time.Duration) {
	if g.metrics[name] == nil {
		g.metrics[name] = &histogram{}
	}
	g.metrics[name].add(d)
}
func (g *guildRun) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// validateAccounting rejects vacuous runs and every selected attempt needs a terminal.
func (g *guildRun) validateAccounting() error {
	if g.selected == 0 || g.announced == 0 {
		return errors.New("insufficient exercise: no selected/audible local track")
	}
	terminals := 0
	for _, n := range g.terminal {
		terminals += n
	}
	if terminals != g.selected {
		return errors.New("selected attempt lacks terminal outcome")
	}
	if g.accepted != g.selected+len(g.expected) {
		return errors.New("unaccounted logical track")
	}
	return nil
}
func atomicJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Run uses only an owned SQLite file and controlled subprocess/transport boundaries.
func Run(parent context.Context, c Config) (result error) {
	if c.Out == "" || c.Duration <= 0 || c.Sample <= 0 || c.Audio < 20*time.Millisecond || c.Audio > time.Minute || (c.Playlist != 123 && c.Playlist != 500) || c.Guilds < 1 || c.Guilds > 8 {
		return errors.New("invalid QA config: out, positive durations, audio 20ms..1m, playlist 123|500, guilds 1..8 required")
	}
	out, err := filepath.Abs(c.Out)
	if err != nil {
		return err
	}
	c.Out = out
	// Refuse an arbitrary existing directory, including any production DB directory.
	marker := filepath.Join(out, "qa010-owned.json")
	if c.Resume {
		b, err := os.ReadFile(marker)
		if err != nil {
			return fmt.Errorf("resume requires owned marker: %w", err)
		}
		var old Config
		if err = json.Unmarshal(b, &old); err != nil {
			return err
		}
		if old.Playlist != c.Playlist || old.Guilds != c.Guilds || old.Seed != c.Seed || old.Warm != c.Warm || old.Faults != c.Faults || old.Audio != c.Audio {
			return errors.New("resume configuration mismatch")
		}
	} else {
		if err = os.Mkdir(out, 0700); err != nil {
			return fmt.Errorf("new QA directory required: %w", err)
		}
		if err = atomicJSON(marker, c); err != nil {
			return err
		}
	}
	// A process-owned advisory lock prevents concurrent segments sharing a database.
	lock, err := lockDirectory(out)
	if err != nil {
		return err
	}
	defer lock()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	wav := filepath.Join(out, "source.wav")
	if err = wave(wav, c.Audio); err != nil {
		return err
	}
	producer := filepath.Join(out, "yt-dlp-local")
	if err = os.WriteFile(producer, []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec "+quote(executable)+" source "+quote(wav)+" \"$@\"\n"), 0700); err != nil {
		return err
	}
	segment := time.Now().UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(out, segment)
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	eventsFile, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return err
	}
	defer eventsFile.Close()
	sampleFile, err := os.Create(filepath.Join(dir, "resources.jsonl"))
	if err != nil {
		return err
	}
	defer sampleFile.Close()
	events, samples := &journal{f: eventsFile}, &journal{f: sampleFile}
	started := time.Now()
	runtime.GC()
	samples.write(resources("baseline_after_gc", started))
	db, err := storage.Open(filepath.Join(out, "qa-only.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(parent, c.Duration)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := media.Resolver{Binary: producer, Cache: &media.SearchCache{}}
	streamer := player.Streamer{YTDLP: producer, FFmpeg: ff, FirstAudioTimeout: 5 * time.Second, ResolveTimeout: 5 * time.Second, DrainTimeout: time.Second}
	var groups []*guildRun
	defer func() {
		for _, g := range groups {
			g.p.Close(context.Background())
		}
	}()
	var workers sync.WaitGroup
	for index := 0; index < c.Guilds; index++ {
		id := fmt.Sprint(index + 1)
		g := &guildRun{id: id, terminal: map[string]int{}, metrics: map[string]*histogram{}, done: make(chan struct{}, c.Playlist)}
		g.p = player.New(context.Background(), log, func(playCtx context.Context, tr media.Track, v player.Voice) error {
			at := time.Now()
			inner, stop := context.WithCancel(playCtx)
			defer stop()
			a := &attempt{selected: at, cancel: stop}
			switch {
			case strings.Contains(tr.URL, "qa_fault=transport"):
				a.fault = "transport"
			case strings.Contains(tr.URL, "qa_fault=cancel"):
				a.fault = "cancel"
			}
			g.mu.Lock()
			g.active = a
			if len(g.expected) == 0 || g.expected[0].SpotifyID != tr.SpotifyID || g.expected[0].VideoID != tr.VideoID {
				g.fail(fmt.Errorf("guild %s unexpected selection %s", id, tr.SpotifyID))
			} else {
				g.expected[0] = media.Track{}
				g.expected = g.expected[1:]
			}
			if !strings.Contains(tr.URL, "v="+tr.VideoID) {
				g.fail(errors.New("selected Video ID changed"))
			}
			g.selected++
			if !g.lastEnd.IsZero() {
				g.observe("transition", at.Sub(g.lastEnd))
			}
			g.mu.Unlock()
			events.write(map[string]any{"event": "selected", "at": at.UTC(), "guild": id, "identity": tr.SpotifyID, "video_id": tr.VideoID, "fault": a.fault})
			playErr := streamer.Play(inner, tr, v)
			status := "completed"
			if playCtx.Err() != nil {
				status = "shutdown_interrupted"
			} else if playErr != nil {
				if a.frames.Load() == 0 {
					status = "unavailable"
				} else {
					status = "error_interrupted"
				}
			}
			g.mu.Lock()
			g.terminal[status]++
			g.frames += int(a.frames.Load())
			g.lastEnd = time.Now()
			g.mu.Unlock()
			message := ""
			if playErr != nil {
				message = playErr.Error()
			}
			events.write(map[string]any{"event": "terminal", "at": time.Now().UTC(), "guild": id, "identity": tr.SpotifyID, "status": status, "frames": a.frames.Load(), "error": message})
			g.done <- struct{}{}
			return playErr
		})
		groups = append(groups, g) // own cleanup even if recovery setup fails
		if err = g.p.ConfigureStore(db, id); err != nil {
			return err
		}
		initial, err := db.LoadGuild(context.Background(), id)
		if err != nil {
			return err
		}
		if initial.Current != nil {
			g.expected = append(g.expected, *initial.Current)
		}
		g.expected = append(g.expected, initial.Queue...)
		g.accepted = len(g.expected)
		g.p.SetOnStart(func(e player.PlaybackEvent) {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.active == nil || g.active.frames.Load() == 0 {
				g.fail(errors.New("announcement before successful audio"))
			}
			if g.active.announced {
				g.fail(errors.New("duplicate start announcement"))
			}
			g.active.announced = true
			g.observe("announcement_after_frame", time.Since(g.active.first))
			g.announced++
			g.observe("first_frame", time.Since(g.active.selected))
			events.write(map[string]any{"event": "announced", "at": time.Now().UTC(), "guild": id, "identity": e.Track.SpotifyID, "sequence": e.Sequence})
		})
		if err = g.p.Attach(&voice{g}); err != nil {
			return err
		}
		workers.Go(func() {
			remaining := g.accepted
			batch := 0
			for {
				for remaining > 0 {
					select {
					case <-g.done:
						remaining--
					case <-ctx.Done():
						return
					}
				}
				if ctx.Err() != nil {
					return
				}
				tracks := make([]media.Track, c.Playlist)
				for i := range tracks {
					// The segment in logical identity prevents accidental duplicates on resume.
					video := fmt.Sprintf("%011d", (index+1)*100000000+batch*c.Playlist+i)
					target := "https://www.youtube.com/watch?v=" + video
					begin := time.Now()
					tr, resolveErr := resolver.Resolve(ctx, target)
					g.mu.Lock()
					g.observe("cache_miss_resolution", time.Since(begin))
					g.mu.Unlock()
					if resolveErr != nil {
						if ctx.Err() == nil {
							g.mu.Lock()
							g.fail(resolveErr)
							g.mu.Unlock()
							cancel()
						}
						return
					}
					if c.Warm {
						begin = time.Now()
						hit, resolveErr := resolver.Resolve(ctx, target)
						g.mu.Lock()
						g.observe("cache_hit_resolution", time.Since(begin))
						g.mu.Unlock()
						if resolveErr != nil || hit.VideoID != tr.VideoID {
							g.mu.Lock()
							g.fail(errors.New("cache changed identity"))
							g.mu.Unlock()
							cancel()
							return
						}
					}
					tr.SpotifyID = fmt.Sprintf("qa/%s/%s/%d/%d", segment, id, batch, i)
					tr.RequestedBy = "qa-local"
					tr.ForcedYouTube = true
					if c.Faults {
						switch (uint64(i) + c.Seed) % 29 {
						case 0:
							tr.URL += "&qa_fault=source"
						case 1:
							tr.URL += "&qa_fault=transport"
						case 2:
							tr.URL += "&qa_fault=cancel"
						}
					}
					tracks[i] = tr
				}
				g.mu.Lock()
				g.expected = append(g.expected, tracks...)
				g.accepted += len(tracks)
				g.batches++
				g.mu.Unlock()
				begin := time.Now()
				enqueueErr := g.p.EnqueueMany(tracks, g.p.Ticket())
				g.mu.Lock()
				g.observe("enqueue", time.Since(begin))
				if enqueueErr != nil {
					g.fail(enqueueErr)
				}
				g.mu.Unlock()
				events.write(map[string]any{"event": "batch_accepted", "at": time.Now().UTC(), "guild": id, "size": len(tracks), "batch": batch})
				if enqueueErr != nil {
					cancel()
					return
				}
				remaining = len(tracks)
				batch++
			}
		})
	}
	ticker := time.NewTicker(c.Sample)
	defer ticker.Stop()
	for ctx.Err() == nil {
		select {
		case <-ticker.C:
			samples.write(resources("periodic", started))
			for _, g := range groups {
				state := g.p.State()
				if state.PersistenceError != "" {
					g.mu.Lock()
					g.fail(errors.New(state.PersistenceError))
					g.mu.Unlock()
					cancel()
				}
			}
			_ = sampleFile.Sync()
			_ = eventsFile.Sync()
		case <-ctx.Done():
		}
	}
	workers.Wait()
	cleanup := time.Now()
	for _, g := range groups {
		g.p.Close(context.Background())
		saved, readErr := db.LoadGuild(context.Background(), g.id)
		g.mu.Lock()
		if readErr != nil {
			g.fail(readErr)
		}
		if err := g.validateAccounting(); err != nil {
			g.fail(err)
		}
		ids := func(ts []media.Track) []string {
			var ids []string
			for _, tr := range ts {
				ids = append(ids, tr.SpotifyID)
			}
			return ids
		}
		if !reflect.DeepEqual(ids(g.expected), ids(saved.Queue)) {
			g.fail(errors.New("persisted pending queue changed order"))
		}
		g.mu.Unlock()
	}
	if err = db.Close(); err != nil {
		return err
	}
	runtime.GC()
	after := resources("after_cleanup_gc", started)
	samples.write(after)
	if n, ok := after["child_processes"].(int); ok && n != 0 {
		result = errors.Join(result, fmt.Errorf("owned child processes after cleanup: %d", n))
	}
	summary := map[string]any{"segment": segment, "config": c, "go_version": runtime.Version(), "started_at": started.UTC(), "real_elapsed_seconds": time.Since(started).Seconds(), "target_seconds": c.Duration.Seconds(), "continuous_target_completed": errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil && time.Since(started) >= c.Duration, "cleanup_ms": float64(time.Since(cleanup)) / float64(time.Millisecond), "status": "pass", "cache_memory_bytes": nil, "discord_e2e": false, "guilds": []any{}}
	for _, g := range groups {
		m := map[string]any{}
		for name, h := range g.metrics {
			m[name] = h.summary()
		}
		row := map[string]any{"guild": g.id, "accepted": g.accepted, "selected": g.selected, "pending": len(g.expected), "announced": g.announced, "frames": g.frames, "batches": g.batches, "terminal": g.terminal, "latencies": m}
		if g.err != nil {
			row["error"] = g.err.Error()
			result = errors.Join(result, g.err)
		}
		summary["guilds"] = append(summary["guilds"].([]any), row)
	}
	if parent.Err() != nil {
		summary["status"] = "interrupted"
		result = errors.Join(result, parent.Err())
	}
	if events.err != nil || samples.err != nil {
		result = errors.Join(result, events.err, samples.err)
	}
	if result != nil {
		summary["status"] = "failed"
		summary["error"] = result.Error()
	}
	if err = eventsFile.Sync(); err != nil {
		result = errors.Join(result, err)
	}
	if err = sampleFile.Sync(); err != nil {
		result = errors.Join(result, err)
	}
	if result != nil {
		summary["status"] = "failed"
		summary["error"] = result.Error()
	}
	if err = atomicJSON(filepath.Join(dir, "summary.json"), summary); err != nil {
		return err
	}
	fmt.Printf("QA segment %s status=%s elapsed=%.3fs\n", segment, summary["status"], summary["real_elapsed_seconds"])
	return result
}
