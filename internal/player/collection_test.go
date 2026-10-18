package player

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whiterun/internal/media"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !condition() {
		select {
		case <-deadline:
			t.Fatal("condition did not complete")
		case <-time.After(time.Millisecond):
		}
	}
}
func firstResult(t *testing.T, j *CollectionJob) CollectionFirst {
	t.Helper()
	select {
	case r := <-j.First:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("first result blocked")
		return CollectionFirst{}
	}
}
func jobDone(t *testing.T, j *CollectionJob) {
	t.Helper()
	select {
	case <-j.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("collection workers leaked")
	}
}
func matched(track media.Track) media.Track {
	track.URL = "https://youtube.com/watch?v=abcdefghijk"
	return track
}
func begin(t *testing.T, p *GuildPlayer, ctx context.Context, tracks []media.Track, match CollectionMatcher) *CollectionJob {
	t.Helper()
	j, err := p.BeginCollection(ctx, media.Collection{Tracks: tracks, IsCollection: true}, match, p.Ticket())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestProgressiveFirstStartsWith122Unresolved(t *testing.T) {
	started := make(chan string, 124)
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	gate := make(chan struct{})
	var active, maximum, background atomic.Int32
	j := begin(t, p, context.Background(), numberedTracks(1, 123), func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if track.Title == "1" {
			if !first || background.Load() != 0 {
				t.Error("first track lost priority")
			}
			return matched(track), nil
		}
		if first {
			t.Error("background used first-track capacity")
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		background.Add(1)
		select {
		case <-gate:
			return matched(track), nil
		case <-ctx.Done():
			return media.Track{}, ctx.Err()
		}
	})
	first := firstResult(t, j)
	if first.Err != nil || first.Position != 1 {
		t.Fatal(first)
	}
	receive(t, started, "1")
	await(t, func() bool { return background.Load() == 3 })
	state := p.State()
	if len(state.Entries) != 122 || len(state.Queued) != 0 || len(state.Resolving) != 1 || state.Resolving[0].Resolved != 1 {
		t.Fatalf("wrong pending snapshot: %+v", state.Resolving)
	}
	for _, entry := range state.Entries {
		if !entry.Pending {
			t.Fatal("unresolved track exposed")
		}
	}
	// Pause does not stop background loading.
	if _, err := p.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	close(gate)
	await(t, func() bool { return len(p.State().Queued) == CollectionPrefetch })
	if maximum.Load() > CollectionWorkers || active.Load() != 0 {
		t.Fatal("worker bound/leak")
	}
	if state = p.State(); !state.Paused || len(state.Queued) != CollectionPrefetch || len(state.Resolving) != 1 {
		t.Fatal("paused loading exceeded prefetch window")
	}
}

func TestProgressiveOutOfOrderContiguousPrefix(t *testing.T) {
	started := make(chan string, 5)
	p, _ := newTestPlayer(t, func(ctx context.Context, t media.Track, _ Voice) error {
		started <- t.Title
		<-ctx.Done()
		return ctx.Err()
	})
	gates := make([]chan struct{}, 6)
	for i := 2; i <= 5; i++ {
		gates[i] = make(chan struct{})
	}
	j := begin(t, p, context.Background(), numberedTracks(1, 5), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
		n, _ := strconv.Atoi(track.Title)
		if n > 1 {
			select {
			case <-gates[n]:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
		}
		return matched(track), nil
	})
	if r := firstResult(t, j); r.Err != nil {
		t.Fatal(r.Err)
	}
	receive(t, started, "1")
	close(gates[4])
	close(gates[3])
	close(gates[5])
	await(t, func() bool { s := p.State(); return len(s.Resolving) == 1 && s.Resolving[0].Resolved == 4 })
	state := p.State()
	if len(state.Queued) != 0 {
		t.Fatal("later completions exposed before position 2")
	}
	for i, e := range state.Entries {
		if !e.Pending || e.Position != i+2 {
			t.Fatal("pending order corrupted")
		}
	}
	state.Entries[0].Track.Artists[0] = "mutated"
	if p.State().Entries[0].Track.Artists[0] != "Artist" {
		t.Fatal("snapshot aliases job metadata")
	}
	close(gates[2])
	jobDone(t, j)
	for i, tq := range p.State().Queued {
		if tq.Title != fmt.Sprint(i+2) {
			t.Fatal("completion reordered queue")
		}
	}
	for i := 2; i <= 5; i++ {
		p.Skip()
		receive(t, started, fmt.Sprint(i))
	}
}

func TestProgressiveFailureSemantics(t *testing.T) {
	for _, failures := range [][]int{{1}, {3}, {2, 4}, {1, 2, 3, 4, 5}} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			started := make(chan string, 5)
			p, _ := newTestPlayer(t, func(ctx context.Context, t media.Track, _ Voice) error {
				started <- t.Title
				<-ctx.Done()
				return ctx.Err()
			})
			failed := map[string]bool{}
			for _, n := range failures {
				failed[fmt.Sprint(n)] = true
			}
			j := begin(t, p, context.Background(), numberedTracks(1, 5), func(_ context.Context, track media.Track, _ bool) (media.Track, error) {
				if failed[track.Title] {
					return media.Track{}, media.ErrSpotifyMatch
				}
				return matched(track), nil
			})
			r := firstResult(t, j)
			jobDone(t, j)
			if len(failures) == 5 {
				if r.Err == nil || p.Length() != 0 || p.State().Current != nil {
					t.Fatal("all-failed accepted")
				}
				return
			}
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			for i := 1; i <= 5; i++ {
				if failed[fmt.Sprint(i)] {
					continue
				}
				receive(t, started, fmt.Sprint(i))
				p.Skip()
			}
			if j.progress.Failed != len(failures) || j.progress.Resolved != 5-len(failures) {
				t.Fatal("incorrect final failure statistics", j.progress)
			}
		})
	}
}

func TestProgressiveCancellation(t *testing.T) {
	for _, operation := range []string{"stop", "clear", "leave", "context", "close"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan string, 1)
			var active atomic.Int32
			p, v := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
				started <- track.Title
				<-ctx.Done()
				return ctx.Err()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			j := begin(t, p, ctx, numberedTracks(1, 123), func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
				if first {
					return matched(track), nil
				}
				active.Add(1)
				defer active.Add(-1)
				<-ctx.Done()
				return media.Track{}, ctx.Err()
			})
			if r := firstResult(t, j); r.Err != nil {
				t.Fatal(r.Err)
			}
			receive(t, started, "1")
			await(t, func() bool { return active.Load() == 3 })
			switch operation {
			case "stop":
				p.Stop()
			case "clear":
				if n := p.Clear(); n != 122 {
					t.Fatal(n)
				}
			case "leave":
				p.Leave(context.Background())
			case "context":
				cancel()
			case "close":
				p.Close(context.Background())
			}
			jobDone(t, j)
			if active.Load() != 0 || p.Length() != 0 || len(p.State().Resolving) != 0 {
				t.Fatal("cancellation left workers or pending entries")
			}
			if operation == "clear" || operation == "context" {
				if p.State().Current == nil {
					t.Fatal("cancellation stopped committed playback")
				}
			} else if p.State().Current != nil {
				t.Fatal("stop/leave kept current")
			}
			if operation == "leave" {
				v.mu.Lock()
				closed := v.closed
				v.mu.Unlock()
				if !closed {
					t.Fatal("leave did not close transport")
				}
			}
		})
	}
}

func TestProgressiveSkipFiftyWithBoundedPrefetch(t *testing.T) {
	started := make(chan string, 123)
	var blocked atomic.Int32
	release := make(chan struct{})
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	j := begin(t, p, context.Background(), numberedTracks(1, 123), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
		n, _ := strconv.Atoi(track.Title)
		if n > 20 {
			blocked.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
		}
		return matched(track), nil
	})
	if r := firstResult(t, j); r.Err != nil {
		t.Fatal(r.Err)
	}
	receive(t, started, "1")
	await(t, func() bool { return len(p.State().Queued) == CollectionPrefetch })
	r, err := p.SkipN(50)
	if err != nil || r.Skipped != 50 || r.Remaining != 73 || !r.NextPending || r.Next.Title != "51" {
		t.Fatal(r, err)
	}
	await(t, func() bool { return blocked.Load() >= 3 }) // skipped in-flight calls were canceled; workers reached 51+
	state := p.State()
	if len(state.Entries) != 73 || state.Entries[0].Position != 51 {
		t.Fatal("logical positions lost")
	}
	select {
	case n := <-started:
		t.Fatal("played unresolved successor", n)
	default:
	}
	close(release)
	receive(t, started, "51")
	await(t, func() bool { return len(p.State().Queued) == CollectionPrefetch })
	for i, e := range p.State().Entries {
		if e.Track.Title != fmt.Sprint(i+52) {
			t.Fatal("skipped entries resurrected")
		}
	}
}

func TestProgressiveMultiplePlaysReserveOrder(t *testing.T) {
	started := make(chan string, 10)
	gate := make(chan struct{})
	p, _ := newTestPlayer(t, func(ctx context.Context, track media.Track, _ Voice) error {
		started <- track.Title
		<-ctx.Done()
		return ctx.Err()
	})
	a := begin(t, p, context.Background(), numberedTracks(1, 3), func(ctx context.Context, track media.Track, _ bool) (media.Track, error) {
		select {
		case <-gate:
			return matched(track), nil
		case <-ctx.Done():
			return media.Track{}, ctx.Err()
		}
	})
	b := begin(t, p, context.Background(), numberedTracks(4, 6), func(_ context.Context, t media.Track, _ bool) (media.Track, error) { return matched(t), nil })
	firstResult(t, b)
	jobDone(t, b)
	if err := p.Enqueue(media.Track{Title: "7"}, p.Ticket()); err != nil {
		t.Fatal(err)
	}
	select {
	case title := <-started:
		t.Fatal("later play overtook pending collection", title)
	default:
	}
	close(gate)
	firstResult(t, a)
	jobDone(t, a)
	for i := 1; i <= 7; i++ {
		receive(t, started, fmt.Sprint(i))
		p.Skip()
	}
}

func TestProgressiveConcurrentReservationsAndGuildIsolation(t *testing.T) {
	makePlayer := func() *GuildPlayer {
		p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
		return p
	}
	p, other := makePlayer(), makePlayer()
	match := func(ctx context.Context, _ media.Track, _ bool) (media.Track, error) {
		<-ctx.Done()
		return media.Track{}, ctx.Err()
	}
	isolated := begin(t, other, context.Background(), numberedTracks(1, 3), match)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var jobs []*CollectionJob
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := p.BeginCollection(context.Background(), media.Collection{Tracks: numberedTracks(1, 20)}, match, p.Ticket())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			jobs = append(jobs, j)
			mu.Unlock()
		}()
	}
	wg.Wait()
	state := p.State()
	for i, e := range state.Entries {
		if e.CollectionID != uint64(i/20+1) || e.Position != i%20+1 {
			t.Fatal("concurrent reservations interleaved")
		}
	}
	p.Stop()
	for _, j := range jobs {
		jobDone(t, j)
	}
	select {
	case <-isolated.Done:
		t.Fatal("stop crossed guilds")
	default:
	}
	if other.Length() != 3 {
		t.Fatal("other guild lost queue")
	}
	other.Stop()
	jobDone(t, isolated)
}

type performanceSpotify struct{}

func (performanceSpotify) Collection(context.Context, string, string) (media.Collection, error) {
	time.Sleep(10 * time.Millisecond)
	return media.Collection{Tracks: numberedTracks(1, 123)}, nil
}
func TestProgressive123StartupMeasurement(t *testing.T) {
	search := func(ctx context.Context, _ string) (media.Track, error) {
		select {
		case <-time.After(10 * time.Millisecond):
			return media.Track{URL: "metadata-only://target"}, nil
		case <-ctx.Done():
			return media.Track{}, ctx.Err()
		}
	}
	resolver := media.CollectionResolver{Spotify: performanceSpotify{}, Search: search}
	query := "https://open.spotify.com/playlist/35m4wmHSlcS2RNsafX4dEg"
	baseline := time.Now()
	if _, err := resolver.Resolve(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	old := time.Since(baseline)
	frame := make(chan time.Time, 1)
	p := New(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, t media.Track, v Voice) error {
		if err := v.WriteFrame(ctx, []byte{1}); err != nil {
			return err
		}
		frame <- time.Now()
		<-ctx.Done()
		return ctx.Err()
	})
	defer p.Close(context.Background())
	p.Attach(&fakeVoice{})
	start := time.Now()
	c, progressive, err := resolver.Prepare(context.Background(), query)
	if err != nil || !progressive {
		t.Fatal(err)
	}
	j := begin(t, p, context.Background(), c.Tracks, resolver.MatchSpotify)
	firstResult(t, j)
	var elapsed time.Duration
	select {
	case at := <-frame:
		elapsed = at.Sub(start)
	case <-time.After(3 * time.Second):
		t.Fatal("first frame blocked")
	}
	if elapsed >= old/2 {
		t.Fatalf("startup not improved: old=%v new=%v", old, elapsed)
	}
	state := p.State()
	if len(state.Resolving) != 1 || state.Resolving[0].Resolved >= 123 {
		t.Fatal("first frame waited for full playlist")
	}
	t.Logf("123 tracks; fake 10ms metadata + 10ms/YouTube target; old atomic first-playable=%v; progressive first fake audio frame=%v; unresolved at frame=%d", old, elapsed, 123-state.Resolving[0].Resolved)
	p.Stop()
	jobDone(t, j)
}

func TestProgressiveSnapshotsDuringResolutionAndControls(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	gate := make(chan struct{})
	j := begin(t, p, context.Background(), numberedTracks(1, 123), func(ctx context.Context, track media.Track, first bool) (media.Track, error) {
		if !first {
			select {
			case <-gate:
			case <-ctx.Done():
				return media.Track{}, ctx.Err()
			}
		}
		return matched(track), nil
	})
	if r := firstResult(t, j); r.Err != nil {
		t.Fatal(r.Err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				state := p.State()
				previous := 0
				for _, entry := range state.Entries {
					if entry.Position <= previous {
						t.Error("snapshot positions reordered")
					}
					previous = entry.Position
					entry.Track.Artists[0] = "snapshot only"
				}
				_, _ = p.Snapshot()
			}
		}()
	}
	close(gate)
	for range 20 {
		if _, err := p.SkipN(2); err != nil {
			t.Fatal(err)
		}
	}
	p.Clear()
	wg.Wait()
	jobDone(t, j)
	if p.Length() != 0 {
		t.Fatal("late result resurrected a cleared entry")
	}
}

func TestFirstCollectionPositionUnavailableAdvances(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	var attempts atomic.Int32
	j := begin(t, p, context.Background(), numberedTracks(1, 3), func(_ context.Context, t media.Track, _ bool) (media.Track, error) {
		if t.Title == "1" && attempts.Add(1) == 1 {
			return media.Track{}, errors.New("Sign in to confirm you're not a bot")
		}
		return matched(t), nil
	})
	first := firstResult(t, j)
	if first.Track.Title != "2" || first.Position != 2 || first.Skipped != 1 {
		t.Fatal("unavailable position did not advance in order", first)
	}
	jobDone(t, j)
}

func TestCollectionFailureAnnouncesOriginalPositionOnce(t *testing.T) {
	p, _ := newTestPlayer(t, func(ctx context.Context, _ media.Track, _ Voice) error { <-ctx.Done(); return ctx.Err() })
	events := make(chan PlaybackEvent, 5)
	p.SetOnSkip(func(e PlaybackEvent) { events <- e })
	tracks := numberedTracks(1, 3)
	tracks[1].Position = 9
	tracks[1].Total = 12
	tracks[1].Artist = "Original Artist"
	j := begin(t, p, context.Background(), tracks, func(_ context.Context, tr media.Track, _ bool) (media.Track, error) {
		if tr.Title == "2" {
			return media.Track{}, &media.MatchError{Status: media.MatchUncertain, Reason: "ambiguous_candidates"}
		}
		return matched(tr), nil
	})
	firstResult(t, j)
	jobDone(t, j)
	select {
	case e := <-events:
		if e.Track.Position != 9 || e.Track.Artist != "Original Artist" || e.SkippedReason == "" {
			t.Fatal(e)
		}
	default:
		t.Fatal("missing skipped track event")
	}
	select {
	case e := <-events:
		t.Fatal("duplicate skip", e)
	default:
	}
}
