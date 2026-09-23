package lobby

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Pieczasz/terminal-card/internal/game"
)

const (
	defaultBrowseLimit = 20
	// MaxBrowseLimit is the most entries one BrowseLobbies call returns.
	MaxBrowseLimit = 200
)

// BrowseMode filters the browse by whether a table is ranked.
type BrowseMode uint8

const (
	BrowseAny BrowseMode = iota
	BrowseRanked
	BrowseCasual
)

// BrowseEntry is one open public table as the browse screen lists it.
type BrowseEntry struct {
	Code       string
	GameName   string
	Players    int
	MaxPlayers int
	Ranked     bool
	// AvgElo is the table's average rating in its game, unrated seats at the start.
	AvgElo uint32
	// EloDelta is how far AvgElo is from the browsing player's own rating.
	EloDelta int
}

func (e BrowseEntry) hasRoom() bool { return e.Players < e.MaxPlayers }

// BrowseFilter narrows BrowseLobbies. The zero value lists every open public table.
type BrowseFilter struct {
	GameName     string // empty means any
	Mode         BrowseMode
	OnlyWithRoom bool
	Limit        int
}

func (f BrowseFilter) matches(e BrowseEntry) bool {
	if f.GameName != "" && e.GameName != f.GameName {
		return false
	}
	if f.OnlyWithRoom && !e.hasRoom() {
		return false
	}
	switch f.Mode {
	case BrowseRanked:
		return e.Ranked
	case BrowseCasual:
		return !e.Ranked
	case BrowseAny:
	}
	return true
}

// BrowseLobbies lists the public waiting tables matching f, closest in rating to p
// first. A nil p is matched at the starting rating.
func (m *Manager) BrowseLobbies(p *game.Player, f BrowseFilter) []BrowseEntry {
	if m == nil {
		return nil
	}
	lobbies := m.publicLobbies()
	var ratings map[string]uint32
	if p != nil {
		ratings = p.Ratings
	}

	limit := f.limit()
	entries := make([]BrowseEntry, 0, min(len(lobbies), limit))
	for _, l := range lobbies {
		entry, open := l.browseEntry()
		if !open || !f.matches(entry) {
			continue
		}
		delta := int(entry.AvgElo) - int(ratingFor(ratings, entry.GameName))
		entry.EloDelta = max(delta, -delta)
		entries = append(entries, entry)
	}

	slices.SortFunc(entries, func(a, b BrowseEntry) int {
		return cmp.Or(cmp.Compare(a.EloDelta, b.EloDelta), strings.Compare(a.Code, b.Code))
	})
	return entries[:min(len(entries), limit)]
}

func (f BrowseFilter) limit() int {
	if f.Limit <= 0 {
		return defaultBrowseLimit
	}
	return min(f.Limit, MaxBrowseLimit)
}

// browseEntry is false for a table no longer on offer. The cache holds pointers, and
// a miss that scanned just before a table went private or started stores it anyway,
// so the list is only as right as this re-check under the lobby's own lock.
func (l *Lobby) browseEntry() (BrowseEntry, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if !l.onOfferLocked() {
		return BrowseEntry{}, false
	}
	return BrowseEntry{
		Code:       l.code,
		GameName:   l.options.cardGame,
		Players:    1 + len(l.guests),
		MaxPlayers: l.options.maxPlayers,
		Ranked:     l.options.isRanked,
		AvgElo:     l.averageEloLocked(l.options.cardGame),
	}, true
}

// onOfferLocked is whether the table belongs in a browse. Caller holds l.mu.
func (l *Lobby) onOfferLocked() bool {
	return !l.options.isPrivate && l.state == waiting
}

// GameNames is the sorted set of games the public waiting tables play.
func (m *Manager) GameNames() []string {
	if m == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, l := range m.publicLobbies() {
		if name := l.GameName(); name != "" {
			seen[name] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// publicLobbyCacheTTL is what keeps a browse off every lobby's own lock. The window
// is short enough that a new table shows up on the next refresh.
const publicLobbyCacheTTL = 2 * time.Second

// invalidatePublicCache makes the next browse re-scan. Callers are the writes that
// change which tables are on offer, so a new or closed table shows up immediately
// rather than a cache window later.
func (m *Manager) invalidatePublicCache() {
	m.cacheDirty.Store(true)
}

// publicLobbies serves the cache under a read lock and, on a miss, copies
// the lobby set and releases m.mu before touching any l.mu - the same shape as
// Stats. Two simultaneous misses both rescan and the later write wins, so the stored
// list can hold a table that has since gone private or started; browseEntry
// re-checks each one, which is what keeps that out of the browse.
func (m *Manager) publicLobbies() []*Lobby {
	m.mu.RLock()
	if !m.cacheDirty.Load() && time.Since(m.cacheLastUpdated) < publicLobbyCacheTTL {
		lobbies := slices.Clone(m.cachedPublicLobbies)
		m.mu.RUnlock()
		return lobbies
	}
	// Cleared inside the same lock hold that snapshots the lobby set, and before it.
	// CreateLobby and RemoveLobby set the flag while holding m.mu exclusively, so an
	// invalidation either happened before this point - and its lobby is in the
	// snapshot - or lands after, and survives into the next browse. Clearing it after
	// the snapshot instead left a window where a table set the flag, this cleared it,
	// and the snapshot had never seen the table: hidden for the whole TTL.
	m.cacheDirty.Store(false)
	all := slices.Collect(maps.Values(m.lobbies))
	m.mu.RUnlock()

	publicLobbies := make([]*Lobby, 0, len(all))
	for _, l := range all {
		l.mu.RLock()
		if l.onOfferLocked() {
			publicLobbies = append(publicLobbies, l)
		}
		l.mu.RUnlock()
	}

	m.mu.Lock()
	m.cachedPublicLobbies = publicLobbies
	m.cacheLastUpdated = time.Now()
	m.mu.Unlock()

	return slices.Clone(publicLobbies)
}
