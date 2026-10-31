package player

import "time"

// resolutionHorizonLocked walks the actual guild queue, not a collection-local
// window. Unknown/live duration is a barrier until that occurrence is hydrated
// or consumed. No timers or new scheduler own these decisions.
func (p *GuildPlayer) resolutionHorizonLocked(job uint64, position int) (time.Duration, bool) {
	if p.prefetchHorizon <= 0 {
		return 0, true
	} // Standalone embedders may retain count-only prefetch.
	if p.pause.paused() {
		return 0, false
	}
	eta := time.Duration(0)
	if p.current != nil {
		if p.current.Live || p.current.Duration <= 0 {
			return 0, false
		}
		eta = max(0, p.current.Duration-p.confirmedOffset)
	}
	for _, entry := range p.queue {
		if entry.jobID == job && entry.position == position {
			return eta, eta < p.prefetchHorizon
		}
		if entry.Live || entry.Duration <= 0 {
			return 0, false
		}
		eta += entry.Duration
		if eta >= p.prefetchHorizon {
			return eta, false
		}
	}
	return 0, false
}
func (j *CollectionJob) waitHorizon(index int) (time.Duration, bool) {
	for {
		p := j.player
		p.mu.Lock()
		wanted := j.wanted[index] && j.ctx.Err() == nil
		eta, ready := p.resolutionHorizonLocked(j.progress.ID, index)
		p.mu.Unlock()
		if !wanted {
			return 0, false
		}
		if ready {
			return eta, true
		}
		j.publishFirst(CollectionFirst{Deferred: true, Total: len(j.metadata)})
		select {
		case <-j.ctx.Done():
			return 0, false
		case <-j.wake:
		}
	}
}
