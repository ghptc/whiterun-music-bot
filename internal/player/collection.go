package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"whiterun/internal/media"
)

const CollectionWorkers = 3
const CollectionPrefetch = 6
const CollectionResolveTimeout = 90 * time.Second

// Pending positions occupy queue capacity and order, but cannot reach playback.
type queueEntry struct {
	media.Track
	Pending  bool
	jobID    uint64
	position int
}
type QueueEntry struct {
	State        AttemptState
	Track        media.Track
	Pending      bool
	CollectionID uint64
	Position     int // original one-based position, even after failures/skips
}
type CollectionProgress struct {
	Uncertain, MetadataSkipped       int
	ID                               uint64
	Total, Resolved, Failed, Skipped int
}
type CollectionFirst struct {
	Deferred                 bool
	Suspended                bool
	Track                    media.Track
	Position, Total, Skipped int
	Err                      error
}
type CollectionMatcher func(context.Context, media.Track, bool) (media.Track, error)

// CollectionJob is player-owned. Mutable fields below are protected by player.mu;
// metadata is copied once. Only the coordinator publishes ordered prefixes.
type CollectionJob struct {
	First     <-chan CollectionFirst
	Done      <-chan struct{}
	wake      chan struct{}
	firstOnce sync.Once
	first     chan CollectionFirst
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	player    *GuildPlayer
	metadata  []media.Track
	match     CollectionMatcher
	wanted    []bool
	searches  []context.CancelFunc
	recorded  []bool
	progress  CollectionProgress
}

// BeginCollection atomically reserves all positions after complete metadata has
// been validated. Reservation order is the ordering of concurrent accepted plays.
// It performs no HTTP/process work while holding player.mu.
func (p *GuildPlayer) BeginCollection(parent context.Context, c media.Collection, match CollectionMatcher, ticket uint64) (*CollectionJob, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	if p.ctx.Err() != nil || parent.Err() != nil || p.voice == nil || ticket != p.generation {
		p.diagnosticLocked(slog.LevelInfo, "queue_generation_rejected", "requested_generation", ticket, "reason", "stale_or_canceled")
		return nil, errors.New("Playback changed while searching. Ask the Bard again.")
	}
	if len(c.Tracks) == 0 || len(c.Tracks) > media.MaxCollectionTracks || match == nil {
		return nil, media.ErrNoTracks
	}
	if len(c.Tracks) > MaxQueueTracks-len(p.queue) {
		return nil, errors.New("The collection will not fit in the queue (1000 upcoming tracks maximum).")
	}
	ctx, cancel := context.WithCancel(parent)
	p.halted = false
	p.failures = 0
	p.nextJob++
	first := make(chan CollectionFirst, 1)
	done := make(chan struct{})
	j := &CollectionJob{wake: make(chan struct{}, 1), First: first, Done: done, first: first, done: done, ctx: ctx, cancel: cancel, player: p, match: match, metadata: make([]media.Track, len(c.Tracks)), wanted: make([]bool, len(c.Tracks)), searches: make([]context.CancelFunc, len(c.Tracks)), recorded: make([]bool, len(c.Tracks)), progress: CollectionProgress{ID: p.nextJob, Total: len(c.Tracks), MetadataSkipped: c.Skipped}}
	for i, t := range c.Tracks {
		if t.Position == 0 {
			t.Position, t.Total = i+1, len(c.Tracks)
		}
		j.metadata[i] = copyTrack(t)
		j.wanted[i] = true
		p.queue = append(p.queue, queueEntry{Track: copyTrack(t), Pending: true, jobID: j.progress.ID, position: i})
	}
	if p.jobs == nil {
		p.jobs = make(map[uint64]*CollectionJob)
	}
	p.jobs[j.progress.ID] = j
	p.mutationLocked("reserve", "collection_id", j.progress.ID, "reserved_count", len(c.Tracks))
	stop := context.AfterFunc(p.ctx, cancel)
	p.jobWorkers.Add(1)
	go func() { defer p.jobWorkers.Done(); defer stop(); j.run() }()
	p.signal("positions_reserved")
	return j, nil
}
func (p *GuildPlayer) cancelJobsLocked() {
	for _, j := range p.jobs {
		j.cancel()
	}
}
func (p *GuildPlayer) releaseEntryLocked(entry queueEntry) {
	if j := p.jobs[entry.jobID]; j != nil {
		j.wanted[entry.position] = false
		select {
		case j.wake <- struct{}{}:
		default:
		}
	}
}
func (p *GuildPlayer) discardEntryLocked(entry queueEntry) {
	j := p.jobs[entry.jobID]
	if j == nil {
		return
	}
	j.wanted[entry.position] = false
	select {
	case j.wake <- struct{}{}:
	default:
	}
	if cancel := j.searches[entry.position]; cancel != nil {
		cancel()
	}
	for _, wanted := range j.wanted {
		if wanted {
			return
		}
	}
	j.cancel()
}

type collectionResolution struct {
	index   int
	track   media.Track
	err     error
	skipped bool
	blocked bool
}

func (j *CollectionJob) publishFirst(first CollectionFirst) {
	j.firstOnce.Do(func() { j.first <- first })
}

func (j *CollectionJob) resolve(index int, first bool) collectionResolution {
	p := j.player
	var eta time.Duration
	if first {
		var wanted bool
		eta, wanted = j.waitHorizon(index)
		if !wanted {
			return collectionResolution{index: index, skipped: true}
		}
	} else {
		p.mu.Lock()
		var ready bool
		eta, ready = p.resolutionHorizonLocked(j.progress.ID, index)
		p.mu.Unlock()
		if !ready {
			return collectionResolution{index: index, blocked: true}
		}
	}
	probe, admissionErr := p.waitProvider(j.ctx, func() { j.publishFirst(CollectionFirst{Suspended: true, Total: len(j.metadata)}) })
	if admissionErr != nil {
		return collectionResolution{index: index, skipped: true, err: admissionErr}
	}
	p.mu.Lock()
	if p.providerBlocked && probe == "" {
		p.mu.Unlock()
		return collectionResolution{index: index, blocked: true}
	}
	if !j.wanted[index] || j.ctx.Err() != nil {
		if probe != "" {
			p.finishProviderLocked(context.Canceled, probe)
		}
		p.mu.Unlock()
		return collectionResolution{index: index, skipped: true}
	}
	// Recovery may have waited minutes while pause, skip or queue order changed.
	// Recheck the same horizon before spending a newly granted provider trial.
	var within bool
	eta, within = p.resolutionHorizonLocked(j.progress.ID, index)
	if !within {
		if probe != "" {
			p.finishProviderLocked(context.Canceled, probe)
		}
		p.mu.Unlock()
		return collectionResolution{index: index, blocked: true}
	}
	ctx, cancel := context.WithTimeout(media.WithProviderPermit(j.ctx, probe), CollectionResolveTimeout)
	providerGeneration := p.providerGeneration
	j.searches[index] = cancel
	active := 0
	for _, job := range p.jobs {
		for _, search := range job.searches {
			if search != nil {
				active++
			}
		}
	}
	p.mu.Unlock()
	j.event("track_resolution_start", time.Now(), "spotify_id", j.metadata[index].SpotifyID, "position", index+1, "state", "resolving", "concurrent_resolutions", active)
	started := time.Now()
	track, err := safeMatch(j.match, media.WithPlaybackEstimate(ctx, eta), copyTrack(j.metadata[index]), first)
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	p.mu.Lock()
	skipped := !j.wanted[index]
	providerFailure := isProviderFailure(err) && !skipped && j.ctx.Err() == nil
	if providerFailure {
		p.observeProviderLocked(err)
		p.blockProviderLocked(providerFailureCategory(err))
	}
	if probe != "" {
		p.finishProviderLocked(err, probe)
	}
	// An in-flight resolver canceled by an older provider incident may finish
	// after a successful probe. Its cancellation is not a per-track failure.
	blocked := providerFailure || (p.providerBlocked && probe == "") || p.providerGeneration != providerGeneration
	j.searches[index] = nil
	p.mu.Unlock()
	cancel()
	if providerFailure {
		p.announceProvider()
	}
	if blocked {
		j.event("track_resolution_discarded", started, "position", index+1, "reason", "provider_blocked")
		return collectionResolution{index: index, blocked: true}
	}
	if err == nil && j.metadata[index].SpotifyID != "" && track.SpotifyID != j.metadata[index].SpotifyID {
		err = &media.MatchError{Status: media.MatchRejected, Reason: "spotify_identity_mismatch"}
	}
	if err == nil && track.URL == "" {
		err = media.ErrSpotifyMatch
	}
	outcome := "ready"
	if err != nil {
		outcome = resolutionFailure(err)
	}
	if skipped {
		outcome = "discarded"
	}
	j.event("track_resolution_complete", started, "position", j.metadata[index].Position, "spotify_id", j.metadata[index].SpotifyID, "video_id", track.VideoID, "outcome", outcome, "cancel_reason", context.Cause(ctx))
	track.Timing = media.TimingFrom(j.ctx)
	track.Position, track.Total = j.metadata[index].Position, j.metadata[index].Total
	return collectionResolution{index: index, track: track, err: err, skipped: skipped}
}

func (j *CollectionJob) event(name string, start time.Time, fields ...any) {
	p := j.player
	p.mu.Lock()
	defer p.mu.Unlock()
	p.diagnosticLocked(slog.LevelInfo, name, append([]any{"collection_id", j.progress.ID, "elapsed_ms", time.Since(start).Milliseconds()}, fields...)...)
}

// commit updates/removes existing slots; it never appends. A skip/clear/stop can
// remove a slot while its search runs, and late results cannot resurrect it.
func (j *CollectionJob) commit(results []collectionResolution) bool {
	p := j.player
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.persistLocked()
	if p.providerBlocked {
		return true
	}
	if j.ctx.Err() != nil {
		p.diagnosticLocked(slog.LevelInfo, "resolution_commit_discarded", "collection_id", j.progress.ID, "reason", "canceled", "cancel_reason", context.Cause(j.ctx))
		return false
	}
	for _, r := range results {
		found := false
		for i, entry := range p.queue {
			if entry.jobID != j.progress.ID || entry.position != r.index {
				continue
			}
			found = true
			if r.err != nil || r.skipped {
				p.queue = append(p.queue[:i], p.queue[i+1:]...)
				j.wanted[r.index] = false
			} else {
				p.queue[i].Track = copyTrack(r.track)
				p.queue[i].Pending = false
			}
			p.queueVersion++
			p.diagnosticLocked(slog.LevelInfo, "resolution_position_commit", "collection_id", j.progress.ID, "position", entry.Track.Position, "queue_position", i+1, "spotify_id", entry.SpotifyID, "video_id", r.track.VideoID, "playback_eligible", r.err == nil && !r.skipped, "scheduler_notified", true)
			break
		}
		if !found {
			p.diagnosticLocked(slog.LevelInfo, "resolution_commit_discarded", "collection_id", j.progress.ID, "position", r.index+1, "reason", "position_invalidated")
		}
	}
	p.signal("resolution_commit")
	return true
}
func (j *CollectionJob) record(r collectionResolution) {
	p := j.player
	p.mu.Lock()
	if p.providerBlocked {
		p.mu.Unlock()
		return
	}
	if j.recorded[r.index] {
		p.mu.Unlock()
		return
	}
	if !j.wanted[r.index] || j.ctx.Err() != nil {
		r.skipped = true
	}
	j.recorded[r.index] = true
	if r.skipped {
		j.progress.Skipped++
	} else if r.err != nil {
		j.progress.Failed++
		var decision *media.MatchError
		if errors.As(r.err, &decision) && decision.Status == media.MatchUncertain {
			j.progress.Uncertain++
		}
	} else {
		j.progress.Resolved++
	}
	p.mu.Unlock()
	if r.err != nil && !r.skipped && j.ctx.Err() == nil {
		p.announceSkip(j.metadata[r.index], r.err)
	}
	if r.err != nil && !r.skipped && j.ctx.Err() == nil && p.log != nil {
		p.log.Warn("collection position could not be matched; skipping", "stage", "collection_position_failed", "collection_id", j.progress.ID, "position", r.index+1, "spotify_id", j.metadata[r.index].SpotifyID, "state", "unavailable", "reason", resolutionFailure(r.err), "error", r.err)
	}
}
func (j *CollectionJob) run() {
	started := time.Now()
	defer func() {
		p := j.player
		p.mu.Lock()
		// Cancellation discards only unresolved positions; valid committed tracks stay.
		kept := p.queue[:0]
		for _, e := range p.queue {
			if e.jobID != j.progress.ID || !e.Pending {
				kept = append(kept, e)
			}
		}
		clear(p.queue[len(kept):])
		p.queue = kept
		p.persistLocked()
		progress := j.progress
		p.completedImports = []CollectionProgress{progress}
		delete(p.jobs, progress.ID)
		p.signal()
		p.mu.Unlock()
		err := j.ctx.Err()
		if err == nil {
			err = media.ErrSpotifyMatch
		}
		j.publishFirst(CollectionFirst{Err: err, Total: progress.Total, Skipped: progress.Failed + progress.Skipped})
		j.event("background_collection_resolution_complete", started, "spotify_tracks", progress.Total, "youtube_resolved", progress.Resolved, "youtube_failed", progress.Failed, "skipped_positions", progress.Skipped, "unfinished", progress.Total-progress.Resolved-progress.Failed-progress.Skipped, "canceled", j.ctx.Err() != nil)
		j.cancel()
		close(j.done)
	}()
	firstIndex := -1
	for i := 0; i < len(j.metadata); {
		if j.ctx.Err() != nil {
			return
		}
		at := time.Now()
		j.event("first_track_resolution_start", at, "position", i+1)
		r := j.resolve(i, true)
		j.event("first_track_resolution_complete", at, "position", i+1, "success", r.err == nil && !r.skipped)
		if j.ctx.Err() != nil {
			return
		}
		if r.blocked {
			j.publishFirst(CollectionFirst{Suspended: true, Total: len(j.metadata)})
			continue
		}
		j.record(r)
		if !j.commit([]collectionResolution{r}) {
			return
		}
		if r.err != nil || r.skipped {
			i++
			continue
		}
		firstIndex = i
		j.event("first_track_committed", started, "position", i+1)
		j.publishFirst(CollectionFirst{Track: copyTrack(r.track), Position: i + 1, Total: len(j.metadata), Skipped: i})
		break
	}
	if firstIndex < 0 || firstIndex == len(j.metadata)-1 {
		return
	}
	j.event("background_collection_resolution_start", time.Now(), "remaining", len(j.metadata)-firstIndex-1, "workers", CollectionWorkers)
	for {
		p := j.player
		p.mu.Lock()
		indexes := make([]int, 0, CollectionPrefetch)
		remaining, window := 0, 0
		for _, entry := range p.queue {
			if entry.jobID != j.progress.ID {
				continue
			}
			if entry.Pending {
				remaining++
			}
			_, within := p.resolutionHorizonLocked(j.progress.ID, entry.position)
			if window < CollectionPrefetch && within && entry.Pending && j.wanted[entry.position] {
				indexes = append(indexes, entry.position)
			}
			window++
		}
		p.mu.Unlock()
		if remaining == 0 {
			return
		}
		if len(indexes) == 0 {
			j.event("resolver_window_wait", time.Now(), "reason", "prefetch_window_ready", "expected_event", "queue_window_changed", "pending_count", remaining)
			select {
			case <-j.ctx.Done():
				return
			case <-j.wake:
				j.event("resolver_window_wakeup", time.Now(), "wakeup_reason", "queue_window_changed")
			}
			continue
		}
		tasks := make(chan int, len(indexes))
		results := make(chan collectionResolution, len(indexes))
		for _, i := range indexes {
			tasks <- i
		}
		close(tasks)
		var workers sync.WaitGroup
		for range min(CollectionWorkers, len(indexes)) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := range tasks {
					if j.ctx.Err() != nil {
						return
					}
					results <- j.resolve(i, false)
				}
			}()
		}
		buffered := make([]collectionResolution, len(j.metadata))
		ready := make([]bool, len(j.metadata))
		blocked := false
		frontier := 0
		for range indexes {
			var r collectionResolution
			select {
			case <-j.ctx.Done():
				workers.Wait()
				return
			case r = <-results:
			}
			if j.ctx.Err() != nil {
				workers.Wait()
				return
			}
			if r.blocked {
				blocked = true
				continue
			}
			j.record(r)
			buffered[r.index] = r
			ready[r.index] = true
			for frontier < len(indexes) && ready[indexes[frontier]] {
				p.mu.Lock()
				providerBlocked := p.providerBlocked
				p.mu.Unlock()
				if providerBlocked {
					blocked = true
					break
				}
				resolved := buffered[indexes[frontier]]
				if !j.commit([]collectionResolution{resolved}) {
					workers.Wait()
					return
				}
				frontier++
			}
		}
		workers.Wait()
		if blocked {
			continue
		}
		j.event("collection_prefix_committed", started, "through_position", indexes[len(indexes)-1]+1)
	}
}

func safeMatch(match CollectionMatcher, ctx context.Context, track media.Track, first bool) (result media.Track, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("collection matcher panic (%T)", value)
		}
	}()
	return match(ctx, track, first)
}

func resolutionFailure(err error) string {
	if category := providerFailureCategory(err); category != "" {
		return category
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "resolution_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "resolution_canceled"
	}
	if errors.Is(err, media.ErrSpotifyMatch) {
		return "no_reliable_youtube_match"
	}
	return "resolution_infrastructure_failure"
}
