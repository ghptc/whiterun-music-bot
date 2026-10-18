package player

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
			return `if [ "$target" = 2 ] && [ -f ` + quote(challengeFile) + ` ]; then echo "ERROR: Sign in to confirm you're not a bot" >&2; exit 1; fi`
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

// Auth rejection is terminal for this attempt, never a musical mismatch.
func TestProviderChallengeAdvancesWithoutRetryLoop(t *testing.T) {
	r := auditPipeline(t, false, true)
	if len(r.results) != 7 || r.state.ProviderBlocked || r.state.Halted || len(r.state.Entries) != 0 {
		t.Fatalf("queue stalled: %+v", r.state)
	}
	if len(r.frames["1"]) < 3 {
		t.Fatal("first track had no audio")
	}
	for i, result := range r.results[1:] {
		if result.title != strconv.Itoa(i+2) {
			t.Fatal("queue order changed", result)
		}
		if i == 0 {
			if failureCategory(result.err) != "youtube_auth_challenge" || len(r.frames[result.title]) != 0 {
				t.Fatalf("wrong failure: %+v", result)
			}
		} else if result.err != nil || len(r.frames[result.title]) < 3 {
			t.Fatalf("eligible successor failed: %+v", result)
		}
	}
	if strings.Count(r.events, "ff-start") != 6 {
		t.Fatal("decoder launched for rejected extraction", r.events)
	}
	if strings.Count(r.events, "yt-start ") != 7 {
		t.Fatal("unbounded attempts", r.events)
	}
}

func TestProviderFailureDoesNotBlockProgressiveResolution(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	j := begin(t, p, context.Background(), numberedTracks(1, 4), func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if track.Position == 2 {
			return media.Track{}, errors.New("Sign in to confirm you're not a bot")
		}
		return matched(track), nil
	})
	firstResult(t, j)
	jobDone(t, j)
	if j.progress.Failed != 1 || j.progress.Resolved != 3 || p.State().ProviderBlocked {
		t.Fatalf("wrong progress: %+v", j.progress)
	}
	for _, e := range p.State().Entries {
		if e.Position == 2 {
			t.Fatal("unavailable position retained")
		}
	}
}

func TestStopAndLeaveReleaseOldProviderBlock(t *testing.T) {
	for _, operation := range []string{"stop", "leave"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan string, 2)
			p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
				if track.Title == "blocked" {
					return errors.New("Sign in to confirm you're not a bot")
				}
				started <- track.Title
				<-ctx.Done()
				return ctx.Err()
			})
			add(t, p, "blocked")
			await(t, func() bool { return p.State().Current == nil && len(p.State().Entries) == 0 })
			if operation == "stop" {
				p.Stop()
			} else {
				p.Leave(context.Background())
				if err := p.Attach(&fakeVoice{}); err != nil {
					t.Fatal(err)
				}
			}
			if p.State().ProviderBlocked {
				t.Fatal("fresh session retained old provider latch")
			}
			add(t, p, "fresh request")
			receive(t, started, "fresh request")
		})
	}
}
