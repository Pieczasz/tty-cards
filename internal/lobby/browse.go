package lobby

import (
	"slices"
	"strings"

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
