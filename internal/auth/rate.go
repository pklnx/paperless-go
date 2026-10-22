package auth

import (
	"sync"
	"time"
)

// Rate allows a number of events per key in a fixed time window, for
// requests that cost the server something even when they are valid. It
// keeps its counters in memory; a restart resets them.
type Rate struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*rateWindow
	now    func() time.Time
}

type rateWindow struct {
	start time.Time
	n     int
	// refused is set by the first refusal in the window, so a caller can
	// log one line per key and window, not one per refused request.
	refused bool
}

// NewRate allows limit events per key and window.
func NewRate(limit int, window time.Duration) *Rate {
	return &Rate{limit: limit, window: window, counts: map[string]*rateWindow{}, now: time.Now}
}

// SetClock replaces the clock, for a server with an injected clock and for
// tests. Call it before the Rate is used.
func (r *Rate) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Allow counts an event for key. If the limit is reached, it returns false
// and how long until the window ends.
func (r *Rate) Allow(key string) (bool, time.Duration) {
	ok, wait, _ := r.AllowFirst(key)
	return ok, wait
}

// AllowFirst is Allow that also reports, for a refused event, whether it is
// the first refusal of key in its window.
func (r *Rate) AllowFirst(key string) (ok bool, wait time.Duration, first bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.counts[key]
	if w == nil || now.Sub(w.start) >= r.window {
		if len(r.counts) >= sweepEvery {
			for k, old := range r.counts {
				if now.Sub(old.start) >= r.window {
					delete(r.counts, k)
				}
			}
		}
		w = &rateWindow{start: now}
		r.counts[key] = w
	}
	if w.n >= r.limit {
		first = !w.refused
		w.refused = true
		return false, w.start.Add(r.window).Sub(now), first
	}
	w.n++
	return true, 0, false
}

// Blocked returns how long until key may have an event again, without
// counting one; zero means now.
func (r *Rate) Blocked(key string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.counts[key]
	if w == nil || now.Sub(w.start) >= r.window || w.n < r.limit {
		return 0
	}
	return w.start.Add(r.window).Sub(now)
}

// BlockedFirst is Blocked for a caller that refuses the event while key is
// blocked: it also reports whether this is the first refusal of key in its
// window. It counts no event.
func (r *Rate) BlockedFirst(key string) (wait time.Duration, first bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w := r.counts[key]
	if w == nil || now.Sub(w.start) >= r.window || w.n < r.limit {
		return 0, false
	}
	first = !w.refused
	w.refused = true
	return w.start.Add(r.window).Sub(now), first
}
