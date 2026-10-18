package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whiterun/internal/media"
)

// Requirements P1-P4 and V1 in docs/bard-requirements.md. Only provider
// availability and the final voice transport are substituted. Player, queue,
// Streamer, os/exec, pipes, FFmpeg, Opus parsing and pacing are production code.
type auditVoice struct {
	fakeVoice
	mu     sync.Mutex
	frames map[string][][]byte
	first  chan struct{}
	once   sync.Once
}
type auditTrackVoice struct {
	Voice
	sink  *auditVoice
	title string
}

func (v auditTrackVoice) WriteFrame(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := v.Voice.WriteFrame(ctx, frame); err != nil {
		return err
	}
	// The five transmission-ending silence packets are not evidence of media.
	if !bytes.Equal(frame, []byte{0xf8, 0xff, 0xfe}) {
		v.sink.mu.Lock()
		v.sink.frames[v.title] = append(v.sink.frames[v.title], bytes.Clone(frame))
		v.sink.mu.Unlock()
		v.sink.once.Do(func() { close(v.sink.first) })
	}
	return nil
}

type auditResult struct {
	title          string
	err, parentErr error
}
type auditRun struct {
	state        State
	results      []auditResult
	frames       map[string][][]byte
	events, logs string
	player       *GuildPlayer
	resultStream <-chan auditResult
	challenge    string
	eventFile    string
}

func auditPipeline(t *testing.T, progressive, challenge bool) auditRun {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("reliability acceptance tests require FFmpeg with libopus:", err)
	}
	dir := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	for n := 1; n <= 7; n++ {
		file := wav(t)
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i := 44; i+3 < len(data); i += 4 {
			sample := int16(10000 * math.Sin(2*math.Pi*float64(330*n)*float64((i-44)/4)/48000))
			binary.LittleEndian.PutUint16(data[i:], uint16(sample))
			binary.LittleEndian.PutUint16(data[i+2:], uint16(sample))
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.wav", n)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	events, eof, challengeFile := filepath.Join(dir, "events"), filepath.Join(dir, "first-complete"), filepath.Join(dir, "challenge")
	if challenge {
		if err := os.WriteFile(challengeFile, []byte("blocked"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	yt := executable(t, "yt-dlp", `for arg do target="$arg"; done
printf 'yt-start %s\n' "$target" >> `+quote(events)+`
if [ "$target" != 1 ] && [ ! -f `+quote(eof)+` ]; then exit 91; fi
`+func() string {
		if challenge {
			return `if [ "$target" != 1 ] && [ -f ` + quote(challengeFile) + ` ]; then echo "ERROR: Sign in to confirm you're not a bot" >&2; exit 1; fi`
		}
		return ""
	}()+`
cat `+quote(dir)+`/"$target".wav || exit 92
sleep 0.08
printf 'yt-natural %s\n' "$target" >> `+quote(events)+`
exit 0
`)
	decoder := executable(t, "ffmpeg", "printf 'ff-start\\n' >> "+quote(events)+"\nexec "+quote(ff)+" \"$@\"\n")
	stream := Streamer{YTDLP: yt, FFmpeg: decoder}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	sink := &auditVoice{frames: make(map[string][][]byte), first: make(chan struct{})}
	results := make(chan auditResult, 10)
	var logs lockedLog
	p := New(ctx, slog.New(slog.NewJSONHandler(&logs, nil)), func(ctx context.Context, track media.Track, v Voice) error {
		err := stream.Play(ctx, track, auditTrackVoice{Voice: v, sink: sink, title: track.Title})
		if track.Title == "1" && err == nil {
			if e := os.WriteFile(eof, []byte("natural pipeline EOF"), 0600); e != nil {
				err = e
			}
		}
		results <- auditResult{track.Title, err, ctx.Err()}
		return err
	})
	t.Cleanup(func() { p.Close(context.Background()) })
	if err := p.Attach(sink); err != nil {
		t.Fatal(err)
	}
	count := 2
	if challenge {
		count = 7
	}
	tracks := numberedTracks(1, count)
	for i := range tracks {
		tracks[i].URL = strconv.Itoa(i + 1)
	}
	if progressive {
		job, err := p.BeginCollection(ctx, media.Collection{Tracks: tracks}, func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
			if !first {
				select {
				case <-sink.first:
				case <-ctx.Done():
					return media.Track{}, ctx.Err()
				}
			}
			return track, nil
		}, p.Ticket())
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-job.Done:
		case <-ctx.Done():
			t.Fatal("collection did not complete")
		}
	} else if err := p.EnqueueMany(tracks, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	var observed []auditResult
	for {
		state := p.State()
		if len(observed) > 0 && state.Current == nil && (state.Halted || state.ProviderBlocked || len(state.Entries) == 0) {
			break
		}
		select {
		case r := <-results:
			observed = append(observed, r)
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("pipeline stuck", ctx.Err())
		}
	}
	state := p.State()
	for len(results) > 0 {
		observed = append(observed, <-results)
	}
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	return auditRun{state: state, results: observed, frames: sink.frames, events: string(raw), logs: logs.String(), player: p, resultStream: results, challenge: challengeFile, eventFile: events}
}

func TestTwoTracksNaturalEOFDeliverIndependentAudio(t *testing.T) {
	for _, progressive := range []bool{false, true} {
		t.Run(fmt.Sprintf("progressive=%v", progressive), func(t *testing.T) {
			r := auditPipeline(t, progressive, false)
			if len(r.results) != 2 {
				t.Fatalf("play invocations: %d", len(r.results))
			}
			for i, result := range r.results {
				if result.title != strconv.Itoa(i+1) || result.err != nil || result.parentErr != nil {
					t.Fatalf("track %d: %+v", i+1, result)
				}
				if len(r.frames[result.title]) < 3 {
					t.Fatalf("track %s delivered only %d media frames", result.title, len(r.frames[result.title]))
				}
			}
			if bytes.Equal(bytes.Join(r.frames["1"], nil), bytes.Join(r.frames["2"], nil)) {
				t.Fatal("successor audio indistinguishable from first track")
			}
			if strings.Count(r.events, "ff-start") != 2 || strings.Count(r.events, "yt-start") != 2 || strings.Count(r.events, "yt-natural") != 2 {
				t.Fatal(r.events)
			}
			if strings.Index(r.events, "yt-start 2") < strings.Index(r.events, "yt-natural 1") {
				t.Fatal("successor extracted before predecessor EOF")
			}
			if strings.Count(r.logs, `"msg":"next_track_selected"`) != 2 || strings.Count(r.logs, `"msg":"audio_source_exit"`) != 2 || strings.Count(r.logs, `"category":"eof"`) < 4 {
				t.Fatal("missing natural pipeline completion evidence")
			}
			t.Logf("both tracks completed naturally; media frames: first=%d second=%d; distinct audio; two extraction and decoder processes", len(r.frames["1"]), len(r.frames["2"]))
		})
	}
}

// This replays the provider boundary deterministically. It verifies retention,
// blocked status, bounded attempts and explicit recovery of the same position.
func TestProviderChallengeDoesNotConsumeQueue(t *testing.T) {
	r := auditPipeline(t, false, true)
	if len(r.frames["1"]) < 3 || r.results[0].err != nil {
		t.Fatal("precondition: first track did not naturally complete")
	}
	if len(r.results) < 2 || r.results[1].err == nil || r.results[1].parentErr != nil || len(r.frames["2"]) != 0 {
		t.Fatal("provider failure replay did not reach independent second extraction")
	}
	t.Logf("second extraction category=%s; attempts=%d; retained=%d; provider_blocked=%v", failureCategory(r.results[1].err), len(r.results), len(r.state.Entries), r.state.ProviderBlocked)
	if failureCategory(r.results[1].err) != "youtube_auth_challenge" {
		t.Fatalf("expected classified provider challenge, got %q", failureCategory(r.results[1].err))
	}
	if len(r.results) != 2 || len(r.state.Entries) != 6 || !r.state.ProviderBlocked || r.state.Halted {
		t.Fatalf("R3: provider challenge must block and retain failed position without consuming successors: attempts=%d retained=%d blocked=%v halted=%v", len(r.results), len(r.state.Entries), r.state.ProviderBlocked, r.state.Halted)
	}
	for i, entry := range r.state.Entries {
		want := strconv.Itoa(i + 2)
		if entry.Track.Title != want || entry.Position != i+2 {
			t.Fatalf("logical queue changed at %d: got title=%s position=%d want title=%s position=%d", i, entry.Track.Title, entry.Position, want, i+2)
		}
	}
	if strings.Count(r.events, "yt-start ") != 2 || !strings.Contains(r.events, "yt-start 2\n") || strings.Contains(r.events, "yt-start 3\n") {
		t.Fatalf("provider block allowed extra source attempts: %s", r.events)
	}
	if !strings.Contains(r.logs, `"msg":"provider_playback_blocked"`) {
		t.Fatal("provider blocked state was not logged")
	}

	// Explicit /resume policy: remove the deterministic provider rejection and
	// prove the same logical position retries before any successor.
	if err := os.Remove(r.challenge); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.player.SetPaused(false); err != nil || !changed {
		t.Fatalf("resume did not release provider block: changed=%v err=%v", changed, err)
	}
	completed := make([]auditResult, 0, 6)
	deadline := time.After(12 * time.Second)
	for len(completed) < 6 {
		select {
		case result := <-r.resultStream:
			completed = append(completed, result)
		case <-deadline:
			t.Fatalf("recovery did not complete retained queue: %d retry results", len(completed))
		}
	}
	for i, result := range completed {
		if result.title != strconv.Itoa(i+2) || result.err != nil || result.parentErr != nil {
			t.Fatalf("recovery result %d did not retry the same logical position: %+v, error=%v", i+1, result, result.err)
		}
	}
	updatedEvents, err := os.ReadFile(r.eventFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(updatedEvents), "yt-start 2\n") > strings.Index(string(updatedEvents), "yt-start 3\n") {
		t.Fatal("recovery advanced past #2 before retrying it")
	}
}

func TestProviderBlockSuspendsProgressiveResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	playing := make(chan struct{})
	p := New(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(ctx context.Context, _ media.Track, _ Voice) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-playing:
			return nil
		}
	})
	t.Cleanup(func() { p.Close(context.Background()) })
	if err := p.Attach(&fakeVoice{}); err != nil {
		t.Fatal(err)
	}
	tracks := numberedTracks(1, 4)
	for i := range tracks {
		tracks[i].URL = strconv.Itoa(i + 1)
	}
	var calls atomic.Int32
	callPositions := make(chan int, 16)
	challenged := atomic.Bool{}
	challenged.Store(true)
	job, err := p.BeginCollection(ctx, media.Collection{Tracks: tracks}, func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if p.State().ProviderBlocked {
			t.Errorf("background matcher started while provider was blocked at position %d", track.Position)
		}
		calls.Add(1)
		if !first {
			callPositions <- track.Position
		}
		if !first && track.Position == 2 && challenged.Swap(false) {
			return media.Track{}, &StreamError{Category: "youtube_auth_challenge", Err: errors.New("controlled provider challenge")}
		}
		return track, nil
	}, p.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.First:
	case <-ctx.Done():
		t.Fatal("first collection position did not resolve")
	}
	await(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.providerBlocked && job.suspended
	})
	blockedCalls := calls.Load()
	state := p.State()
	for want := 2; want <= 4; want++ {
		found := false
		for _, entry := range state.Entries {
			if entry.Position == want && entry.Pending {
				found = true
			}
		}
		if !found {
			t.Fatalf("provider block changed pending logical position %d: %+v", want, state.Entries)
		}
	}
	if len(state.Entries) < 3 {
		t.Fatalf("provider block changed pending logical positions: %+v", state.Entries)
	}
	select {
	case <-job.Done:
		t.Fatal("progressive resolver completed and discarded pending positions while blocked")
	default:
	}
	for {
		select {
		case <-callPositions:
		default:
			goto drained
		}
	}
drained:
	if changed, err := p.SetPaused(false); err != nil || !changed {
		t.Fatalf("resume failed to release provider block: changed=%v err=%v", changed, err)
	}
	select {
	case position := <-callPositions:
		if position != 2 {
			t.Fatalf("recovery started position %d before retrying blocked position 2", position)
		}
	case <-ctx.Done():
		t.Fatal("recovery did not retry blocked position")
	}
	select {
	case <-job.Done:
	case <-ctx.Done():
		t.Fatal("progressive resolution did not resume")
	}
	state = p.State()
	positionTwoResolved := false
	for _, entry := range state.Entries {
		if entry.Position == 2 && !entry.Pending {
			positionTwoResolved = true
		}
	}
	if state.ProviderBlocked || !positionTwoResolved {
		t.Fatalf("retry did not resolve the retained position: %+v", state)
	}
	if calls.Load() <= blockedCalls {
		t.Fatal("explicit recovery did not restart provider matching")
	}
}
