package sweep

// SetQueueSeams makes s call afterMark after step 1 and afterRemove after
// step 2 of the queue pass, for each hash: a test stops the pass there.
func SetQueueSeams(s *Sweeper, afterMark, afterRemove func(hash string)) {
	s.afterMark, s.afterRemove = afterMark, afterRemove
}
