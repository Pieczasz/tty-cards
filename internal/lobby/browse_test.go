package lobby

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTable(t *testing.T, m *Manager, id string, dbID uuid.UUID, gameName string, rating uint32, opts ...Option) *Lobby {
	t.Helper()
	leader := mockPlayer(id, dbID)
	if rating > 0 {
		leader.Ratings = map[string]uint32{gameName: rating}
	}
	opts = append([]Option{WithPrivate(false), WithCardGame(gameName)}, opts...)
	l, err := m.New(leader, opts...)
	require.NoError(t, err)
	return l
}

func codesOf(entries []BrowseEntry) []string {
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}
	return codes
}

func TestBrowseLobbies_CapsToTheClosestTables(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)

	for i := range 30 {
		rating := uint32(1000 + i*50)
		openTable(t, m, fmt.Sprintf("p%d", i), testutil.UID(uint64(i+1)), "Poker", rating)
	}

	browser := mockPlayer("browser", testutil.UID(999))
	browser.Ratings = map[string]uint32{"Poker": 1500}

	entries := m.BrowseLobbies(browser, BrowseFilter{})

	require.Len(t, entries, DefaultBrowseLimit, "the list is capped")
	assert.LessOrEqual(t, entries[0].EloDelta, entries[len(entries)-1].EloDelta,
		"closest rating first")
	for _, e := range entries {
		assert.LessOrEqual(t, e.EloDelta, 500,
			"a table %d away survived the cap while nearer ones were dropped", e.EloDelta)
	}
}

func TestBrowseLobbies_LimitIsConfigurable(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	for i := range 5 {
		openTable(t, m, fmt.Sprintf("p%d", i), testutil.UID(uint64(i+1)), "Poker", 1500)
	}

	assert.Len(t, m.BrowseLobbies(nil, BrowseFilter{Limit: 2}), 2)
	assert.Len(t, m.BrowseLobbies(nil, BrowseFilter{Limit: -1}), 5, "a nonsense limit falls back to the default")
}

func TestBrowseLobbies_LimitIsHardCapped(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	for i := range MaxBrowseLimit + 10 {
		openTable(t, m, fmt.Sprintf("p%d", i), testutil.UID(uint64(i+1)), "Poker", 1500)
	}

	entries := m.BrowseLobbies(nil, BrowseFilter{Limit: MaxBrowseLimit * 2})
	assert.Len(t, entries, MaxBrowseLimit)
}

func TestBrowseLobbies_Filters(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)

	poker := openTable(t, m, "poker", testutil.UID(1), "Poker", 1500, WithRanked(true))
	eights := openTable(t, m, "eights", testutil.UID(2), "CrazyEights", 1500)
	full := openTable(t, m, "full", testutil.UID(3), "Poker", 1500, WithMaxPlayers(2))
	require.NoError(t, m.JoinLobbyByCode(full.Code(), mockPlayer("filler", testutil.UID(4))))

	tests := []struct {
		name   string
		filter BrowseFilter
		want   []string
	}{
		{
			name:   "no filter shows every open table",
			filter: BrowseFilter{},
			want:   []string{poker.Code(), eights.Code(), full.Code()},
		},
		{
			name:   "by game",
			filter: BrowseFilter{GameName: "CrazyEights"},
			want:   []string{eights.Code()},
		},
		{
			name:   "ranked only",
			filter: BrowseFilter{Mode: BrowseRanked},
			want:   []string{poker.Code()},
		},
		{
			name:   "casual only",
			filter: BrowseFilter{Mode: BrowseCasual},
			want:   []string{eights.Code(), full.Code()},
		},
		{
			name:   "with a seat free",
			filter: BrowseFilter{OnlyWithRoom: true},
			want:   []string{poker.Code(), eights.Code()},
		},
		{
			name:   "filters combine",
			filter: BrowseFilter{GameName: "Poker", OnlyWithRoom: true},
			want:   []string{poker.Code()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.ElementsMatch(t, tt.want, codesOf(m.BrowseLobbies(nil, tt.filter)))
		})
	}
}

func TestBrowseLobbies_RowCarriesWhatTheListShows(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	l := openTable(t, m, "leader", testutil.UID(1), "Poker", 1800, WithRanked(true), WithMaxPlayers(4))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), mockPlayer("guest", testutil.UID(2))))

	entries := m.BrowseLobbies(nil, BrowseFilter{})

	require.Len(t, entries, 1)
	entry := entries[0]
	assert.Equal(t, l.Code(), entry.Code)
	assert.Equal(t, "Poker", entry.GameName)
	assert.Equal(t, 2, entry.Players)
	assert.Equal(t, 4, entry.MaxPlayers)
	assert.True(t, entry.Ranked)
	assert.True(t, entry.HasRoom())
	assert.Equal(t, (1800+elo.ToUint32(elo.DefaultRating))/2, entry.AvgElo)
}

func TestBrowseEntry_HasRoom(t *testing.T) {
	t.Parallel()
	assert.True(t, BrowseEntry{Players: 1, MaxPlayers: 2}.HasRoom())
	assert.False(t, BrowseEntry{Players: 2, MaxPlayers: 2}.HasRoom(), "a full table has no seat")
	assert.False(t, BrowseEntry{Players: 3, MaxPlayers: 2}.HasRoom())
}

func TestBrowseLobbies_UnratedPlayerIsMatchedAtTheStartingRating(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	near := openTable(t, m, "near", testutil.UID(1), "Poker", elo.ToUint32(elo.DefaultRating))
	openTable(t, m, "far", testutil.UID(2), "Poker", 3000)

	entries := m.BrowseLobbies(mockPlayer("newcomer", testutil.UID(9)), BrowseFilter{})

	require.Len(t, entries, 2)
	assert.Equal(t, near.Code(), entries[0].Code)
	assert.Zero(t, entries[0].EloDelta)
}

func TestBrowseLobbies_EqualRatingsKeepAStableOrder(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	for i := range 6 {
		openTable(t, m, fmt.Sprintf("p%d", i), testutil.UID(uint64(i+1)), "Poker", 1500)
	}

	first := codesOf(m.BrowseLobbies(nil, BrowseFilter{}))
	for range 5 {
		assert.Equal(t, first, codesOf(m.BrowseLobbies(nil, BrowseFilter{})),
			"tables on equal ratings must not reshuffle between refreshes")
	}
}

func TestManager_GameNames(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	openTable(t, m, "a", testutil.UID(1), "Poker", 1500)
	openTable(t, m, "b", testutil.UID(2), "CrazyEights", 1500)
	openTable(t, m, "c", testutil.UID(3), "Poker", 1500)

	private := mockPlayer("private", testutil.UID(4))
	_, err := m.New(private, WithPrivate(true), WithCardGame("Hidden"))
	require.NoError(t, err)

	assert.Equal(t, []string{"CrazyEights", "Poker"}, m.GameNames(),
		"sorted, deduplicated, and public only")
}

func TestBrowseLobbies_VisibilityChangeShowsUpImmediately(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	l := openTable(t, m, "leader", testutil.UID(1), "Poker", 1500)
	require.Len(t, m.BrowseLobbies(nil, BrowseFilter{}), 1, "the list is now cached")

	require.NoError(t, l.SetPrivate(l.Leader(), true))
	assert.Empty(t, m.BrowseLobbies(nil, BrowseFilter{}), "a private table is not on offer")

	require.NoError(t, l.SetPrivate(l.Leader(), false))
	assert.Len(t, m.BrowseLobbies(nil, BrowseFilter{}), 1, "and is back the moment it reopens")
}

func TestBrowse_NilManagerIsSafe(t *testing.T) {
	t.Parallel()
	var m *Manager
	assert.Nil(t, m.BrowseLobbies(mockPlayer("p", testutil.UID(1)), BrowseFilter{}))
	assert.Nil(t, m.GameNames())
}

// The cache holds lobby pointers, and a miss that scanned just before a table went
// private or started stores it anyway - the invalidation it raced was already spent.
// Each entry is re-checked under its own lock, so the cache can only be late about a
// new table, never wrong about a closed one.
func TestBrowseLobbies_RechecksACachedTable(t *testing.T) {
	t.Parallel()

	flips := map[string]func(l *Lobby){
		"went private": func(l *Lobby) { l.options.isPrivate = true },
		"started":      func(l *Lobby) { l.state = InGame },
	}
	for name, flip := range flips {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, nil)
			l := openTable(t, m, "p1", testutil.UID(1), "Poker", 0)
			require.Len(t, m.BrowseLobbies(nil, BrowseFilter{}), 1, "the table is cached as public")

			// Behind the cache's back, the way a concurrent miss leaves it.
			l.mu.Lock()
			flip(l)
			l.mu.Unlock()

			assert.Empty(t, m.BrowseLobbies(nil, BrowseFilter{}), "a cached table was offered after it %s", name)
		})
	}
}
