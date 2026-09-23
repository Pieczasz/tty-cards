package lobby

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Pieczasz/terminal-card/internal/game"
)

const (
	DefaultBrowseLimit = 20
	MaxBrowseLimit     = 200
)

type BrowseMode uint8

const (
	BrowseAny BrowseMode = iota
	BrowseRanked
	BrowseCasual
)

type BrowseEntry struct {
	Code       string
	GameName   string
	Players    int
	MaxPlayers int
	Ranked     bool
	AvgElo     uint32
	EloDelta   int
}

func (e BrowseEntry) HasRoom() bool { return e.Players < e.MaxPlayers }

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
	if f.OnlyWithRoom && !e.HasRoom() {
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

func (m *Manager) BrowseLobbies(p *game.Player, f BrowseFilter) []BrowseEntry {
	if m == nil {
		return nil
	}
	lobbies := m.getCachedPublicLobbies()
	var ratings map[string]uint32
	if p != nil {
		ratings = p.Ratings
	}

	entries := make([]BrowseEntry, 0, min(len(lobbies), f.limit()))
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
		if a.EloDelta != b.EloDelta {
			return a.EloDelta - b.EloDelta
		}
		return strings.Compare(a.Code, b.Code)
	})

	if len(entries) > f.limit() {
		entries = entries[:f.limit()]
	}
	return entries
}

func (f BrowseFilter) limit() int {
	if f.Limit <= 0 {
		return DefaultBrowseLimit
	}
	return min(f.Limit, MaxBrowseLimit)
}

// browseEntry is false for a table no longer on offer. The cache holds pointers, and
// a miss that scanned just before a table went private or started stores it anyway,
// so the list is only as right as this re-check under the lobby's own lock.
func (l *Lobby) browseEntry() (BrowseEntry, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.options.isPrivate || l.state != Waiting {
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

func (m *Manager) GameNames() []string {
	if m == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, l := range m.getCachedPublicLobbies() {
		if name := l.GameName(); name != "" {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// publicLobbyCacheTTL is what keeps a browse off every lobby's own lock. The window
// is short enough that a new table shows up on the next refresh.
const publicLobbyCacheTTL = 2 * time.Second

// invalidatePublicCache makes the next browse re-scan. Callers are the writes that
// change which tables are on offer, so a new or closed table shows up immediately
// rather than a cache window later.
func (m *Manager) invalidatePublicCache() {
	if m != nil {
		m.cacheDirty.Store(true)
	}
}

// getCachedPublicLobbies serves the cache under a read lock and, on a miss, copies
// the lobby set and releases m.mu before touching any l.mu - the same shape as
// Stats. Two simultaneous misses both rescan and the later write wins, so the stored
// list can hold a table that has since gone private or started; browseEntry
// re-checks each one, which is what keeps that out of the browse.
func (m *Manager) getCachedPublicLobbies() []*Lobby {
	m.mu.RLock()
	if !m.cacheDirty.Load() && time.Since(m.cacheLastUpdated) < publicLobbyCacheTTL {
		lobbies := slices.Clone(m.cachedPublicLobbies)
		m.mu.RUnlock()
		return lobbies
	}
	// Cleared inside the same lock hold that snapshots the lobby set, and before it.
	// New and RemoveLobby set the flag while holding m.mu exclusively, so an
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
		if !l.options.isPrivate && l.state == Waiting {
			publicLobbies = append(publicLobbies, l)
		}
		l.mu.RUnlock()
	}

	m.mu.Lock()
	m.cachedPublicLobbies = publicLobbies
	m.cacheLastUpdated = time.Now()
	m.mu.Unlock()

	lobbies := slices.Clone(publicLobbies)
	return lobbies
}
