package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	d "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"whiterun/internal/media"
	"whiterun/internal/perfgate"
	"whiterun/internal/player"
	"whiterun/internal/storage"
)

type instantTrace struct {
	mu       sync.Mutex
	start    time.Time
	stages   map[string]int64
	done     chan struct{}
	readyEOF chan struct{}
	once     sync.Once
}

func newInstantTrace() *instantTrace {
	return &instantTrace{start: time.Now(), stages: map[string]int64{"received": 0}, done: make(chan struct{}), readyEOF: make(chan struct{})}
}
func (tr *instantTrace) mark(stage string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if _, ok := tr.stages[stage]; !ok {
		tr.stages[stage] = time.Since(tr.start).Nanoseconds()
	}
	if stage == "transport_sent" {
		tr.once.Do(func() { close(tr.done) })
	}
}
func (tr *instantTrace) snapshot() map[string]int64 {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := map[string]int64{}
	for k, v := range tr.stages {
		out[k] = v
	}
	return out
}

type instantHandler struct {
	shared *instantLog
	attrs  []slog.Attr
}
type instantLog struct {
	sync.Mutex
	traces          map[string]*instantTrace
	eof             map[string]*instantTrace
	processes, peak atomic.Int64
}

func (h instantHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h instantHandler) WithGroup(string) slog.Handler            { return h }
func (h instantHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return instantHandler{h.shared, append(append([]slog.Attr{}, h.attrs...), a...)}
}
func (h instantHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{}
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
	switch r.Message {
	case "yt_dlp_start", "ffmpeg_start":
		n := h.shared.processes.Add(1)
		for old := h.shared.peak.Load(); n > old; old = h.shared.peak.Load() {
			if h.shared.peak.CompareAndSwap(old, n) {
				break
			}
		}
	case "yt_dlp_complete", "ffmpeg_complete":
		h.shared.processes.Add(-1)
	}
	id := fmt.Sprint(attrs["interaction_id"])
	h.shared.Lock()
	tr := h.shared.traces[id]
	next := h.shared.eof[id]
	if r.Message == "playback_complete" && next != nil {
		next.mu.Lock()
		next.start = time.Now()
		next.stages = map[string]int64{"ready_eof": 0}
		next.mu.Unlock()
		close(next.readyEOF)
		delete(h.shared.eof, id)
	}
	h.shared.Unlock()
	if tr == nil {
		return nil
	}
	stage := ""
	switch r.Message {
	case "discord_acknowledged":
		stage = "ack"
	case "candidate_parsing_complete", "resolver_cache_hit":
		if _, ok := tr.snapshot()["metadata"]; !ok {
			tr.mark("metadata")
		}
	case "spotify_metadata_available":
		if count, err := strconv.ParseInt(fmt.Sprint(attrs["tracks"]), 10, 64); err == nil {
			tr.mu.Lock()
			tr.stages["metadata_tracks"] = count
			tr.mu.Unlock()
		}
		stage = "metadata"
	case "play_preparation_complete":
		stage = "preparation"
		if _, ok := tr.snapshot()["metadata"]; !ok {
			tr.mark("metadata")
		}
	case "resolver_complete", "play_first_track_resolved":
		stage = "first_resolved"
	case "stream_resolve_complete":
		stage = "extraction"
	case "ffmpeg_start":
		stage = "ffmpeg_started"
	case "first_opus_packet":
		stage = "opus"
	case "first_discord_send":
		stage = "transport_sent"
	case "voice_join_complete":
		stage = "voice_connected"
	}
	if stage != "" {
		tr.mark(stage)
	}
	return nil
}

type instantScenario struct {
	Name, Kind, Mode                                        string
	Count, Guilds                                           int
	Disconnected, Restart, Shuffle, Transition, Progressive bool
}

// Explicit tags for resources (avoid encoding combined Go fields under one key).
type instantResources struct {
	MaxProcesses     int64  `json:"max_processes"`
	MaxMatches       int64  `json:"max_matches"`
	GoroutinesBefore int    `json:"goroutines_before"`
	GoroutinesAfter  int    `json:"goroutines_after"`
	AllocatedBytes   uint64 `json:"allocated_bytes"`
}

type instantSpotify struct {
	count int
	first media.Track
}

func (s instantSpotify) Collection(ctx context.Context, kind, id string) (media.Collection, error) {
	tracks := make([]media.Track, s.count)
	for i := range tracks {
		tracks[i] = s.first
		tracks[i].Position = i + 1
		tracks[i].Total = s.count
		if i > 0 {
			tracks[i].SpotifyID = fmt.Sprintf("%022d", i)
			tracks[i].Title = fmt.Sprintf("Background fixture %d", i)
		}
	}
	return media.Collection{Tracks: tracks, IsCollection: kind != "track"}, ctx.Err()
}

func instantTools(tb testing.TB) (string, string, media.Track) {
	tb.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		tb.Fatal("performance gate requires real FFmpeg/libopus:", err)
	}
	dir := tb.TempDir()
	audio := filepath.Join(dir, "source.opus")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "0.1", "-ac", "2", "-c:a", "libopus", "-page_duration", "20000", audio).CombinedOutput(); err != nil {
		tb.Fatalf("generate audio: %v %s", err, out)
	}
	raw, err := os.ReadFile(instantCandidatePath())
	if err != nil {
		tb.Fatal(err)
	}
	var c map[string]any
	if err = json.Unmarshal(raw, &c); err != nil {
		tb.Fatal(err)
	}
	c["entries"] = []any{cClone(c)}
	raw, err = json.Marshal(c)
	if err != nil {
		tb.Fatal(err)
	}
	metadata := filepath.Join(dir, "metadata.json")
	if err = os.WriteFile(metadata, raw, 0600); err != nil {
		tb.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\ncase \" $* \" in\n*' --dump-single-json '*) cat " + quote(metadata) + " ;;\n*) printf '\\nBARD_STREAM_RESOLVED MOIwxOYdhrk\\n' >&2; exec cat " + quote(audio) + " ;;\nesac\n"
	yt := filepath.Join(dir, "controlled-yt-dlp")
	if err = os.WriteFile(yt, []byte(script), 0700); err != nil {
		tb.Fatal(err)
	}
	first := media.Track{SpotifyID: "3bC1ahPIYt1btJzSSEyyrF", Title: "Whistle", Artist: "Flo Rida", Artists: []string{"Flo Rida"}, Album: "Wild Ones", Duration: 225 * time.Second, RequestedSource: "spotify", SourceURL: "https://open.spotify.com/track/3bC1ahPIYt1btJzSSEyyrF"}
	return yt, ff, first
}
func cClone(c map[string]any) map[string]any {
	d := map[string]any{}
	for k, v := range c {
		d[k] = v
	}
	return d
}

func instantInteraction(tb testing.TB, id, guild snowflake.ID, kind string, force bool) *events.ApplicationCommandInteractionCreate {
	tb.Helper()
	query := "https://open.spotify.com/" + kind + "/3bC1ahPIYt1btJzSSEyyrF"
	if kind == "youtube" {
		query = "https://www.youtube.com/watch?v=MOIwxOYdhrk"
	}
	body := map[string]any{"id": id.String(), "application_id": "2", "type": 2, "token": "fixture", "guild_id": guild.String(), "user": map[string]any{"id": "3"}, "data": map[string]any{"id": "4", "name": "play", "type": 1, "options": []map[string]any{{"name": "query", "type": 3, "value": query}, {"name": "force", "type": 5, "value": force}}}}
	raw, _ := json.Marshal(body)
	var i d.ApplicationCommandInteraction
	if err := json.Unmarshal(raw, &i); err != nil {
		tb.Fatal(err)
	}
	return &events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: i, Respond: func(d.InteractionResponseType, d.InteractionResponseData, ...rest.RequestOpt) error { return nil }}
}

type instantFixture struct {
	b              *Bot
	db             *storage.DB
	logs           *instantLog
	matching, peak atomic.Int64
	closeOnce      sync.Once
	playEntered    chan struct{}
	releasePlay    chan struct{}
	playOnce       sync.Once
}

func newInstantFixture(tb testing.TB, s instantScenario, yt, ff string, first media.Track) *instantFixture {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &instantLog{traces: map[string]*instantTrace{}, eof: map[string]*instantTrace{}}
	log := slog.New(instantHandler{shared: logs})
	dbPath := filepath.Join(tb.TempDir(), "gate.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		tb.Fatal(err)
	}
	b := &Bot{ctx: ctx, cancel: cancel, log: log, store: db, client: &bot.Client{ApplicationID: 2, Rest: &recordingRest{}, Caches: cache.New(cache.WithCaches(cache.FlagGuilds, cache.FlagVoiceStates, cache.FlagMembers)), VoiceManager: lifecycleManager{}}, guilds: map[snowflake.ID]*guild{}, searches: make(chan struct{}, 4), voiceWake: make(chan struct{}, 1), streamer: player.Streamer{YTDLP: yt, FFmpeg: ff}}
	f := &instantFixture{b: b, db: db, logs: logs, playEntered: make(chan struct{}), releasePlay: make(chan struct{})}
	b.resolver = media.CollectionResolver{Spotify: instantSpotify{s.Count, first}, YouTube: media.Resolver{Binary: yt, Cache: &media.SearchCache{}, Mappings: db}, Limiter: media.NewMatchLimiter(), Log: log}
	b.resolver.Search = func(ctx context.Context, q string) (media.Track, error) {
		n := f.matching.Add(1)
		defer f.matching.Add(-1)
		for old := f.peak.Load(); n > old; old = f.peak.Load() {
			if f.peak.CompareAndSwap(old, n) {
				break
			}
		}
		if strings.Contains(q, "Background fixture") {
			<-ctx.Done()
			return media.Track{}, ctx.Err()
		}
		r := b.resolver.YouTube
		r.Match = &first
		return r.Resolve(ctx, q)
	}
	if s.Mode == "warm" {
		kind := s.Kind
		if kind == "force" {
			kind = "youtube"
		}
		e := instantInteraction(tb, 999, 1, kind, s.Kind == "force")
		query := e.SlashCommandInteractionData().String("query")
		if _, _, err = b.resolver.PreparePlay(ctx, query, s.Kind == "force"); err != nil {
			tb.Fatal("prime metadata:", err)
		}
		if kind != "youtube" {
			if _, err = b.resolver.MatchSpotify(ctx, first, true); err != nil {
				tb.Fatal("prime first match:", err)
			}
		}
	}
	if s.Restart {
		if err = db.Close(); err != nil {
			tb.Fatal(err)
		}
		db, err = storage.Open(dbPath)
		if err != nil {
			tb.Fatal(err)
		}
		f.db = db
		b.store = db
		b.resolver.YouTube.Mappings = db
		b.resolver.YouTube.Cache = &media.SearchCache{}
	}
	for i := 1; i <= s.Guilds; i++ {
		id := snowflake.ID(i)
		g := b.getGuild(id)
		if g == nil {
			tb.Fatal("guild load failed")
		}
		if s.Shuffle {
			g.player.Close(context.Background())
			g.player = player.New(ctx, log.With("guild_id", id), func(ctx context.Context, tr media.Track, v player.Voice) error {
				f.playOnce.Do(func() { close(f.playEntered) })
				select {
				case <-f.releasePlay:
				case <-ctx.Done():
					return ctx.Err()
				}
				return b.play(ctx, tr, v)
			})
			if err = g.player.ConfigureStore(db, id.String()); err != nil {
				tb.Fatal(err)
			}
			b.bindAnnouncements(g)
		}
		listener(b, id, 3, false)
		if !s.Disconnected {
			c := &lifecycleConn{}
			if err = g.player.Attach(transport{conn: c}); err != nil {
				tb.Fatal(err)
			}
			g.conn = c
			g.channel = 5
		}
	}
	tb.Cleanup(f.close)
	return f
}
func (f *instantFixture) close() {
	f.closeOnce.Do(func() {
		f.b.cancel()
		f.b.workers.Wait()
		for _, g := range f.b.guilds {
			g.player.Close(context.Background())
		}
		_ = f.db.Close()
		f.b = nil
		f.db = nil
		f.logs = nil
	})
}

func instantScenarios() []instantScenario {
	var out []instantScenario
	for _, base := range []instantScenario{{Name: "spotify-single", Kind: "track", Count: 1}, {Name: "youtube-single", Kind: "youtube", Count: 1}, {Name: "spotify-album", Kind: "album", Count: 16}, {Name: "playlist-10", Kind: "playlist", Count: 10}, {Name: "playlist-50", Kind: "playlist", Count: 50}, {Name: "playlist-123", Kind: "playlist", Count: 123}, {Name: "playlist-302", Kind: "playlist", Count: 302}, {Name: "playlist-500", Kind: "playlist", Count: 500}, {Name: "youtube-force", Kind: "force", Count: 1}, {Name: "voice-disconnected", Kind: "track", Count: 1, Disconnected: true}, {Name: "after-restart", Kind: "track", Count: 1, Restart: true}, {Name: "after-shuffle", Kind: "playlist", Count: 50, Shuffle: true}, {Name: "progressive-active", Kind: "playlist", Count: 500, Progressive: true}} {
		for _, mode := range []string{"cold", "warm"} {
			base.Mode = mode
			base.Guilds = 1
			out = append(out, base)
		}
	}
	for _, n := range []int{2, 5, 10} {
		out = append(out, instantScenario{Name: fmt.Sprintf("load-%d-guilds", n), Kind: "playlist", Count: 500, Guilds: n, Mode: "warm"})
	}
	return out
}

func TestInstantPlaybackGate(t *testing.T) {
	if os.Getenv("BARD_INSTANT_GATE") != "1" {
		t.Skip("dedicated performance job: BARD_INSTANT_GATE=1; real FFmpeg required")
	}
	samples := perfgate.MinimumSamples
	if raw := os.Getenv("BARD_GATE_SAMPLES"); raw != "" {
		var err error
		samples, err = strconv.Atoi(raw)
		if err != nil || samples < perfgate.MinimumSamples {
			t.Fatal("gate requires at least 100 samples")
		}
	}
	yt, ff, first := instantTools(t)
	for _, s := range instantScenarios() {
		t.Run(s.Name+"/"+s.Mode, func(t *testing.T) {
			var samplesNS []map[string]int64
			var values []time.Duration
			var maxProc, maxMatches int64
			before := runtime.NumGoroutine()
			var cpuBefore, cpuAfter syscall.Rusage
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
			var peakHeap uint64
			var peakGoroutines int
			var memBefore, memAfter runtime.MemStats
			runtime.ReadMemStats(&memBefore)
			sampleResources := func() {
				runtime.ReadMemStats(&memAfter)
				if memAfter.HeapAlloc > peakHeap {
					peakHeap = memAfter.HeapAlloc
				}
				if n := runtime.NumGoroutine(); n > peakGoroutines {
					peakGoroutines = n
				}
			}
			for repeat := 0; repeat < samples; repeat++ {
				f := newInstantFixture(t, s, yt, ff, first)
				traces := make([]*instantTrace, s.Guilds)
				events := make([]*events.ApplicationCommandInteractionCreate, s.Guilds)
				for i := range traces {
					id := snowflake.ID(100 + i)
					kind := s.Kind
					if kind == "force" {
						kind = "youtube"
					}
					events[i] = instantInteraction(t, id, snowflake.ID(i+1), kind, s.Kind == "force")
					traces[i] = newInstantTrace()
					f.logs.Lock()
					f.logs.traces[id.String()] = traces[i]
					f.logs.Unlock()
				}
				var wg sync.WaitGroup
				barrier := make(chan struct{})
				for i := range traces {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-barrier
						traces[i].mu.Lock()
						traces[i].start = time.Now()
						traces[i].mu.Unlock()
						f.b.commandAt(events[i], traces[i].start)
					}(i)
				}
				close(barrier)
				if s.Shuffle {
					select {
					case <-f.playEntered:
					case <-time.After(8 * time.Second):
						f.close()
						t.Fatal("shuffle playback never selected")
					}
					if err := f.b.getGuild(1).player.Shuffle(); err != nil {
						f.close()
						t.Fatal(err)
					}
					close(f.releasePlay)
				}
				deadline := time.NewTimer(8 * time.Second)
				for _, tr := range traces {
					select {
					case <-tr.done:
						sampleResources()
					case <-deadline.C:
						f.close()
						t.Fatal("no first frame: command response is not playback", s)
					}
				}
				deadline.Stop()
				wg.Wait()
				if s.Progressive {
					state := f.b.getGuild(1).player.State()
					if len(state.Resolving) == 0 || len(state.Entries) < s.Count-1 {
						f.close()
						t.Fatal("progressive import drained synchronously before first frame")
					}
				}
				for guildIndex, tr := range traces {
					stages := tr.snapshot()
					stages["guild_index"] = int64(guildIndex + 1)
					for _, key := range []string{"received", "ack", "metadata", "first_resolved", "extraction", "ffmpeg_started", "opus", "transport_sent"} {
						if _, ok := stages[key]; !ok {
							t.Fatal("missing monotonic stage", key, stages)
						}
					}
					samplesNS = append(samplesNS, stages)
					values = append(values, time.Duration(stages["transport_sent"]))
				}
				if n := f.logs.peak.Load(); n > maxProc {
					maxProc = n
				}
				if n := f.peak.Load(); n > maxMatches {
					maxMatches = n
				}
				logs := f.logs
				f.close()
				if logs.processes.Load() != 0 {
					t.Fatal("subprocess lifecycle not drained", logs.processes.Load())
				}
				runtime.ReadMemStats(&memAfter)
				if memAfter.HeapAlloc > peakHeap {
					peakHeap = memAfter.HeapAlloc
				}
				if n := runtime.NumGoroutine(); n > peakGoroutines {
					peakGoroutines = n
				}
				if f.matching.Load() != 0 {
					t.Fatal("matching goroutine leaked")
				}
			}
			runtime.ReadMemStats(&memAfter)
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
			cpuUS := func(r syscall.Rusage) int64 {
				return r.Utime.Sec*1000000 + r.Utime.Usec + r.Stime.Sec*1000000 + r.Stime.Usec
			}
			budget := 5 * time.Second
			if s.Mode == "warm" {
				budget = 2 * time.Second
			}
			if s.Transition {
				budget = time.Second
			}
			status := "PASS"
			if err := perfgate.Check(values, budget); err != nil {
				status = "FAIL"
				t.Error(err)
			}
			if s.Guilds > 1 {
				for guildIndex := 1; guildIndex <= s.Guilds; guildIndex++ {
					var perGuild []time.Duration
					for _, sample := range samplesNS {
						if sample["guild_index"] == int64(guildIndex) {
							perGuild = append(perGuild, time.Duration(sample["transport_sent"]))
						}
					}
					if err := perfgate.Check(perGuild, budget); err != nil {
						status = "FAIL"
						t.Errorf("guild %d: %v", guildIndex, err)
					}
				}
			}
			if maxProc > int64(2*s.Guilds+4) {
				status = "FAIL"
				t.Error("subprocess bound exceeded", maxProc)
			}
			if runtime.NumGoroutine() > before+4 {
				status = "FAIL"
				t.Error("goroutines did not drain", before, runtime.NumGoroutine())
			}
			if maxMatches > 4 {
				status = "FAIL"
				t.Error("MatchLimiter exceeded four slots", maxMatches)
			}
			report := map[string]any{"scenario": s, "ttfa": perfgate.Summarize(values), "budget_ms": budget.Milliseconds(), "status": status, "environment": "deterministic-dispatch-real-ffmpeg-controlled-ytdlp-simulated-udp", "monotonic_ns": samplesNS, "cpu_ms": float64(cpuUS(cpuAfter)-cpuUS(cpuBefore)) / 1000, "peak_sampled_heap_bytes": peakHeap, "peak_sampled_goroutines": peakGoroutines, "resources": instantResources{maxProc, maxMatches, before, runtime.NumGoroutine(), memAfter.TotalAlloc - memBefore.TotalAlloc}}
			raw, _ := json.Marshal(report)
			t.Log("PERF_GATE_JSON " + string(raw))
		})
	}
}

// Keep the fixture source generation and metadata extraction reusable by Go benches.
func BenchmarkInstantCachedResolution(b *testing.B) {
	yt, _, first := instantTools(b)
	r := media.Resolver{Binary: yt, Cache: &media.SearchCache{}, Match: &first}
	if _, err := r.Resolve(context.Background(), "Flo Rida Whistle"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Resolve(context.Background(), "Flo Rida Whistle"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestInstantPlaybackTransitionGate(t *testing.T) {
	if os.Getenv("BARD_INSTANT_GATE") != "1" {
		t.Skip("dedicated performance job")
	}
	yt, ff, first := instantTools(t)
	var values []time.Duration
	var stages []map[string]int64
	for range perfgate.MinimumSamples {
		s := instantScenario{Name: "ready-transition", Kind: "youtube", Count: 1, Guilds: 1, Mode: "warm"}
		f := newInstantFixture(t, s, yt, ff, first)
		current, next := newInstantTrace(), newInstantTrace()
		f.logs.Lock()
		f.logs.traces["100"] = current
		f.logs.traces["222"] = next
		f.logs.eof["100"] = next
		f.logs.Unlock()
		e := instantInteraction(t, 100, 1, "youtube", false)
		f.b.commandAt(e, current.start)
		select {
		case <-current.done:
		case <-time.After(8 * time.Second):
			f.close()
			t.Fatal("first stream missing")
		}
		state := f.b.getGuild(1).player.State()
		if state.Current == nil {
			f.close()
			t.Fatal("source ended before ready next track")
		}
		track := *state.Current
		track.Timing = media.NewTiming(f.b.log.With("interaction_id", snowflake.ID(222)))
		if err := f.b.getGuild(1).player.Enqueue(track, f.b.getGuild(1).player.Ticket()); err != nil {
			f.close()
			t.Fatal(err)
		}
		select {
		case <-next.done:
		case <-time.After(8 * time.Second):
			f.close()
			t.Fatal("ready EOF transition stalled")
		}
		sample := next.snapshot()
		for _, key := range []string{"ready_eof", "extraction", "ffmpeg_started", "opus", "transport_sent"} {
			if _, ok := sample[key]; !ok {
				f.close()
				t.Fatal("missing transition stage", key)
			}
		}
		values = append(values, time.Duration(sample["transport_sent"]))
		stages = append(stages, sample)
		f.close()
	}
	status := "PASS"
	if err := perfgate.Check(values, time.Second); err != nil {
		status = "FAIL"
		t.Error(err)
	}
	raw, _ := json.Marshal(map[string]any{"scenario": instantScenario{Name: "ready-transition", Kind: "youtube", Count: 2, Guilds: 1, Mode: "ready", Transition: true}, "metric": "ready_eof_to_transport", "ttfa": perfgate.Summarize(values), "budget_ms": 1000, "status": status, "environment": "deterministic-dispatch-real-ffmpeg-controlled-ytdlp-simulated-udp", "monotonic_ns": stages})
	t.Log("PERF_GATE_JSON " + string(raw))
}

func BenchmarkInstantUncachedResolution(b *testing.B) {
	yt, _, first := instantTools(b)
	r := media.Resolver{Binary: yt, Match: &first}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Resolve(context.Background(), "Flo Rida Whistle"); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkInstantPlaylistPreparation(b *testing.B) {
	yt, ff, first := instantTools(b)
	for _, n := range []int{10, 50, 123, 302, 500} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s := instantScenario{Name: "prepare", Kind: "playlist", Count: n, Guilds: 1, Mode: "cold"}
			f := newInstantFixture(b, s, yt, ff, first)
			defer f.close()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := f.b.resolver.PreparePlay(context.Background(), "https://open.spotify.com/playlist/3bC1ahPIYt1btJzSSEyyrF", false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkInstantStreamingStartup(b *testing.B) {
	yt, ff, _ := instantTools(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		logs := &instantLog{traces: map[string]*instantTrace{}, eof: map[string]*instantTrace{}}
		tr := newInstantTrace()
		logs.traces["1"] = tr
		track := media.Track{URL: "https://www.youtube.com/watch?v=MOIwxOYdhrk", Timing: media.NewTiming(slog.New(instantHandler{shared: logs}).With("interaction_id", "1"))}
		done := make(chan error, 1)
		go func() {
			done <- (player.Streamer{YTDLP: yt, FFmpeg: ff}).Play(ctx, track, transport{conn: &lifecycleConn{}})
		}()
		select {
		case <-tr.done:
		case err := <-done:
			cancel()
			b.Fatal("no first frame", err)
		case <-time.After(8 * time.Second):
			cancel()
			b.Fatal("startup timeout")
		}
		cancel()
		<-done
	}
}
func BenchmarkInstantTimeToFirstFrame(b *testing.B) {
	yt, ff, first := instantTools(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		f := newInstantFixture(b, instantScenario{Name: "ttfa", Kind: "track", Count: 1, Guilds: 1, Mode: "warm"}, yt, ff, first)
		tr := newInstantTrace()
		f.logs.Lock()
		f.logs.traces["100"] = tr
		f.logs.Unlock()
		e := instantInteraction(b, 100, 1, "track", false)
		b.StartTimer()
		tr.start = time.Now()
		f.b.commandAt(e, tr.start)
		select {
		case <-tr.done:
		case <-time.After(8 * time.Second):
			f.close()
			b.Fatal("no first frame")
		}
		b.StopTimer()
		f.close()
		b.StartTimer()
	}
}

// External observations only. This cannot approve a Discord/DAVE release gate.
func TestInstantPlaybackProvidersLive(t *testing.T) {
	if os.Getenv("BARD_PROVIDER_PROBE") != "1" {
		t.Skip("explicit isolated provider probe")
	}
	realYT, err := exec.LookPath("yt-dlp")
	if err != nil {
		t.Fatal("real yt-dlp required", err)
	}
	yt, ff, first := instantTools(t)
	cases := []struct {
		Name, Query string
		Count       int
		Force       bool
	}{
		{"youtube-normal", "https://www.youtube.com/watch?v=MOIwxOYdhrk", 1, false},
		{"youtube-force", "https://www.youtube.com/watch?v=MOIwxOYdhrk", 1, true},
	}
	if os.Getenv("SPOTIFY_CLIENT_ID") != "" && os.Getenv("SPOTIFY_CLIENT_SECRET") != "" {
		cases = append(cases, struct {
			Name, Query string
			Count       int
			Force       bool
		}{"spotify-single", "https://open.spotify.com/track/3bC1ahPIYt1btJzSSEyyrF", 1, false})
	}
	for _, c := range cases {
		for _, mode := range []string{"cold", "warm"} {
			t.Run(c.Name+"/"+mode, func(t *testing.T) {
				var values []time.Duration
				var traces []map[string]int64
				var errors []string
				for range 3 {
					s := instantScenario{Name: c.Name, Kind: "youtube", Count: 1, Guilds: 1, Mode: "cold"}
					f := newInstantFixture(t, s, yt, ff, first)
					f.b.streamer.YTDLP = realYT
					f.b.resolver.Search = nil
					f.b.resolver.YouTube.Binary = realYT
					f.b.resolver.Spotify = &media.Spotify{ClientID: os.Getenv("SPOTIFY_CLIENT_ID"), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN"), Log: f.b.log}
					if mode == "warm" {
						if _, _, err := f.b.resolver.PreparePlay(context.Background(), c.Query, c.Force); err != nil {
							errors = append(errors, "provider warmup failed")
							f.close()
							continue
						}
					}
					e := instantInteraction(t, 100, 1, "youtube", c.Force)
					data := e.SlashCommandInteractionData()
					for i := range data.Options {
						if data.Options[i].Name == "query" {
							option := data.Options[i]
							encoded, _ := json.Marshal(c.Query)
							option.Value = encoded
							data.Options[i] = option
						}
					}
					e.Data = data
					tr := newInstantTrace()
					f.logs.Lock()
					f.logs.traces["100"] = tr
					f.logs.Unlock()
					commandDone := make(chan struct{})
					go func() { defer close(commandDone); f.b.commandAt(e, tr.start) }()
					select {
					case <-tr.done:
						values = append(values, time.Duration(tr.snapshot()["transport_sent"]))
						traces = append(traces, tr.snapshot())
					case <-time.After(90 * time.Second):
						errors = append(errors, "no transport frame within 90s")
					}
					f.close()
					<-commandDone
				}
				raw, _ := json.Marshal(map[string]any{"scenario": instantScenario{Name: c.Name, Kind: c.Name, Count: c.Count, Guilds: 1, Mode: mode}, "ttfa": perfgate.Summarize(values), "status": "OBSERVATION", "environment": "real-providers-simulated-udp", "monotonic_ns": traces, "errors": errors, "release_approved": false})
				t.Log("PERF_GATE_JSON " + string(raw))
			})
		}
	}
}

func instantCandidatePath() string {
	if root := os.Getenv("BARD_GATE_FIXTURES"); root != "" {
		return filepath.Join(root, "production/MOIwxOYdhrk.json")
	}
	return "../media/testdata/production/MOIwxOYdhrk.json"
}

func BenchmarkInstantReadyTransition(b *testing.B) {
	yt, ff, first := instantTools(b)
	var elapsed time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		f := newInstantFixture(b, instantScenario{Name: "ready-transition", Kind: "youtube", Count: 1, Guilds: 1, Mode: "warm"}, yt, ff, first)
		current, next := newInstantTrace(), newInstantTrace()
		f.logs.Lock()
		f.logs.traces["100"] = current
		f.logs.traces["222"] = next
		f.logs.eof["100"] = next
		f.logs.Unlock()
		f.b.commandAt(instantInteraction(b, 100, 1, "youtube", false), current.start)
		select {
		case <-current.done:
		case <-time.After(8 * time.Second):
			f.close()
			b.Fatal("no first frame")
		}
		state := f.b.getGuild(1).player.State()
		if state.Current == nil {
			f.close()
			b.Fatal("source ended")
		}
		track := *state.Current
		track.Timing = media.NewTiming(f.b.log.With("interaction_id", snowflake.ID(222)))
		if err := f.b.getGuild(1).player.Enqueue(track, f.b.getGuild(1).player.Ticket()); err != nil {
			f.close()
			b.Fatal(err)
		}
		select {
		case <-next.readyEOF:
		case <-time.After(8 * time.Second):
			f.close()
			b.Fatal("no source EOF")
		}
		b.StartTimer()
		select {
		case <-next.done:
		case <-time.After(8 * time.Second):
			f.close()
			b.Fatal("transition stalled")
		}
		b.StopTimer()
		elapsed += time.Duration(next.snapshot()["transport_sent"])
		f.close()
		b.StartTimer()
	}
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "eof_to_frame_ns/op")
}
