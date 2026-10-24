package store

import (
	"testing"
	"time"
)

// A writer that met a fresh mark (most likely a live pass's) waits a few
// seconds; an older one until a crashed pass's mark would be taken over,
// at least a second.
func TestMarkRetryAfter(t *testing.T) {
	marked := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for now, want := range map[time.Time]time.Duration{
		marked:                                  freshMarkRetry,
		marked.Add(freshMark - time.Nanosecond): freshMarkRetry,
		marked.Add(freshMark):                   MarkStale + markRound - freshMark,
		marked.Add(MarkStale):                   markRound,
		marked.Add(MarkStale + markRound):       time.Second,
		marked.Add(time.Hour):                   time.Second,
	} {
		if got := markRetryAfter(marked, now); got != want {
			t.Errorf("at %v: %v, want %v", now.Sub(marked), got, want)
		}
	}
}
