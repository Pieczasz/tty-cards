package ratelimit

// WithMaxKeys caps the number of tracked keys; production always runs at the default.
func (s *SlidingWindow) WithMaxKeys(n int) *SlidingWindow {
	if n > 0 {
		s.maxKeys = n
	}
	return s
}

// Size is the number of keys currently tracked.
func (s *SlidingWindow) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.logs)
}

// EvictVisits is how many keys eviction has looked at while choosing victims.
func (s *SlidingWindow) EvictVisits() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictVisits
}
