// Package ratelimit holds the per-network sliding-window limiter shared by the ssh
// auth and registration paths, the stats API and the lobby join path, and NetKey,
// which folds an address into the network the limiter counts.
package ratelimit

import (
	"slices"
	"sync"
	"time"
)

const (
	defaultMaxKeys = 10_000
	// sweepEvery amortises the only full-table walk left over that many calls.
	sweepEvery = 64
)

// SlidingWindow admits at most limit calls per key in any window-long span. It is
// safe for concurrent use and bounds its table at defaultMaxKeys keys.
type SlidingWindow struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	logs    map[string][]time.Time
	ops     uint64
	maxKeys int
	// evictVisits counts the keys eviction looked at while choosing a victim: the
	// seam the "does not walk the table" test reads instead of a wall clock.
	evictVisits uint64
}

// New returns a limiter admitting limit calls per key per window.
func New(limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		limit:   limit,
		window:  window,
		logs:    make(map[string][]time.Time),
		maxKeys: defaultMaxKeys,
	}
}

// Allow records a call for key and reports whether it is within the budget. A
// refused call is not recorded, so a caller that backs off recovers.
func (s *SlidingWindow) Allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	threshold := now.Add(-s.window)

	timestamps := filterExpired(s.logs[key], threshold)
	if len(timestamps) == 0 {
		delete(s.logs, key)
	} else {
		s.logs[key] = timestamps
	}

	if len(timestamps) >= s.limit {
		return false
	}

	if _, exists := s.logs[key]; !exists && len(s.logs) >= s.maxKeys {
		s.evictOneLocked()
	}

	s.logs[key] = append(timestamps, now)

	s.ops++
	if s.ops%sweepEvery == 0 {
		s.evictExpiredLocked(threshold)
	}
	return true
}

// filterExpired drops timestamps at or before threshold in place. Both callers
// reassign the result, so reusing the backing array costs nothing and keeps the hot
// path allocation-free.
func filterExpired(timestamps []time.Time, threshold time.Time) []time.Time {
	return slices.DeleteFunc(timestamps, func(t time.Time) bool { return !t.After(threshold) })
}

func (s *SlidingWindow) evictExpiredLocked(threshold time.Time) {
	for key, timestamps := range s.logs {
		valid := filterExpired(timestamps, threshold)
		if len(valid) == 0 {
			delete(s.logs, key)
		} else {
			s.logs[key] = valid
		}
	}
}

// evictOneLocked drops a single key in map order, which is O(1) and is the whole
// point: a full table is reached by a flood of fresh addresses (one IPv6 /48 has
// 65536 networks to spend), and picking the "best" victim meant walking all
// maxKeys entries under the mutex on every one of those requests - the flood paid
// for with our CPU. Go randomizes map iteration, so this is an arbitrary victim,
// not LRU; during a flood any eviction is cheaper than walking the table.
func (s *SlidingWindow) evictOneLocked() {
	for key := range s.logs {
		s.evictVisits++
		delete(s.logs, key)
		return
	}
}
