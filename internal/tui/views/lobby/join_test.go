package lobby

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	"uuid"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openPublicTable(t *testing.T, m *lobby.Manager, id string, dbID uuid.UUID, gameName string, opts ...lobby.Option) *lobby.Lobby {
	t.Helper()
	leader := &game.Player{ID: id, UserID: dbID, Name: id}
	opts = append([]lobby.Option{lobby.WithPrivate(false), lobby.WithCardGame(gameName)}, opts...)
	l, err := m.New(leader, opts...)
	require.NoError(t, err)
	return l
}

func newJoinModel(t *testing.T, m *lobby.Manager) *joinModel {
	t.Helper()
	global := router.GlobalContext{
		User:         testUser(99, "browser"),
		LobbyManager: m,
		GameRegistry: testRegistry(),
		Width:        120,
		Height:       40,
	}
	model, ok := NewJoin(global).(*joinModel)
	require.True(t, ok)
	return model
}

// pressJoin sends a key to the browser; press is typed to the in-lobby view.
func pressJoin(t *testing.T, m *joinModel, key string) {
	t.Helper()
	updated, _ := m.Update(keyMsg(key))
	_, ok := updated.(*joinModel)
	require.True(t, ok)
}

// The list is built per refresh, so a new table has to arrive on the tick.
func TestJoin_RefreshPicksUpNewTables(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	view := newJoinModel(t, m)
	require.Empty(t, view.entries, "nothing is open yet")

	openPublicTable(t, m, "host", testutil.UID(1), testGameName)

	updated, cmd := view.Update(refreshMsg{owner: view})

	require.NotNil(t, cmd, "the refresh must reschedule itself or the list freezes")
	browser, ok := updated.(*joinModel)
	require.True(t, ok)
	assert.Len(t, browser.entries, 1, "a table opened since the last refresh shows up")
}

// A table in play is not joinable, so it leaves the list.
func TestJoin_RefreshDropsTablesThatStarted(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	l := openPublicTable(t, m, "host", testutil.UID(1), testGameName, lobby.WithMaxPlayers(2))
	guest := &game.Player{ID: "guest", UserID: testutil.UID(2), Name: "guest"}
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	view := newJoinModel(t, m)
	view.filter = lobby.BrowseFilter{}
	view.refresh()
	require.Len(t, view.entries, 1)

	registry := testRegistry()
	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	// Starting a game arms the lobby's own result watcher, which lives until the
	// engine's feed closes. Nothing else in this test ends the match, so without this
	// the goroutine outlives the package and goleak fails the whole run.
	t.Cleanup(func() {
		if engine := l.ActiveGame(); engine != nil {
			engine.Close()
		}
	})

	view.refresh()

	assert.Empty(t, view.entries, "a table in play is not one you can join")
}

// Rows vanish under the cursor, so it is clamped on every refresh.
func TestJoin_CursorSurvivesAShrinkingList(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	tables := make([]*lobby.Lobby, 0, 3)
	for i, name := range []string{"a", "b", "c"} {
		tables = append(tables, openPublicTable(t, m, name, testutil.UID(byte(i+1)), testGameName))
	}

	view := newJoinModel(t, m)
	require.Len(t, view.entries, 3)
	pressJoin(t, view, "j")
	pressJoin(t, view, "j")
	require.Equal(t, 2, view.cursor)

	for _, l := range tables {
		m.RemoveLobby(l.Code())
	}

	view.refresh()

	assert.Zero(t, view.cursor, "the cursor cannot point past the end of an empty list")
	require.NotPanics(t, func() { view.joinSelected() }, "and joining nothing is a no-op")
}

func TestJoin_FiltersCycle(t *testing.T) {
	t.Parallel()

	// Each case gets its own table set: a filter left set by the previous one would
	// silently change what the next is counting.
	newBrowser := func(t *testing.T) *joinModel {
		t.Helper()
		m := lobby.NewManager(context.Background(), nil)
		openPublicTable(t, m, "poker", testutil.UID(1), "Poker")
		openPublicTable(t, m, "eights", testutil.UID(2), testGameName, lobby.WithRanked(true))
		view := newJoinModel(t, m)
		require.Len(t, view.entries, 2)
		return view
	}

	t.Run("game steps through every game on offer and back to any", func(t *testing.T) {
		t.Parallel()
		view := newBrowser(t)
		games := view.games
		require.Equal(t, []string{testGameName, "Poker"}, games, "sorted, so the cycle is predictable")

		pressJoin(t, view, "g")
		assert.Equal(t, games[0], view.filter.GameName)
		assert.Len(t, view.entries, 1, "narrowed to one game")

		pressJoin(t, view, "g")
		assert.Equal(t, games[1], view.filter.GameName)

		pressJoin(t, view, "g")
		assert.Empty(t, view.filter.GameName, "past the last game it comes back to any")
		assert.Len(t, view.entries, 2)
	})

	t.Run("mode cycles any, ranked, casual", func(t *testing.T) {
		t.Parallel()
		view := newBrowser(t)

		pressJoin(t, view, "m")
		assert.Equal(t, lobby.BrowseRanked, view.filter.Mode)
		require.Len(t, view.entries, 1, "only the ranked table matches")
		assert.True(t, view.entries[0].Ranked)

		pressJoin(t, view, "m")
		assert.Equal(t, lobby.BrowseCasual, view.filter.Mode)
		require.Len(t, view.entries, 1)
		assert.False(t, view.entries[0].Ranked)

		pressJoin(t, view, "m")
		assert.Equal(t, lobby.BrowseAny, view.filter.Mode)
		assert.Len(t, view.entries, 2)
	})

	t.Run("seats toggles between joinable and every table", func(t *testing.T) {
		t.Parallel()
		view := newBrowser(t)
		require.True(t, view.filter.OnlyWithRoom, "the browser opens on joinable tables only")

		pressJoin(t, view, "o")
		assert.False(t, view.filter.OnlyWithRoom)

		pressJoin(t, view, "o")
		assert.True(t, view.filter.OnlyWithRoom)
	})
}

// Waiting for the next tick would read as the key not working.
func TestJoin_FilterAppliesImmediatelyAndResetsTheCursor(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	openPublicTable(t, m, "poker", testutil.UID(1), "Poker")
	openPublicTable(t, m, "eights", testutil.UID(2), testGameName)

	view := newJoinModel(t, m)
	pressJoin(t, view, "j")
	require.Equal(t, 1, view.cursor)

	pressJoin(t, view, "g")

	assert.Zero(t, view.cursor, "the list changed, so the cursor goes back to the top")
	assert.Len(t, view.entries, 1, "and the narrower list is already on screen")
}

// A filter pinned to a game with no tables shows an empty list for no visible reason.
func TestJoin_GameFilterWithNoTablesFallsBackToAny(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	view := newJoinModel(t, m)
	view.filter.GameName = "Poker"

	view.cycleGame()

	assert.Empty(t, view.filter.GameName)
}

// The mode tag is how a player tells ranked from casual before joining. Codes stay
// off this screen: JoinLobbyByCode does not check privacy, so a browsed code would
// still work after the leader flipped the table private.
func TestJoin_ViewShowsTheColumns(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	ranked := openPublicTable(t, m, "poker", testutil.UID(1), "Poker", lobby.WithRanked(true), lobby.WithMaxPlayers(4))

	view := newJoinModel(t, m)
	rendered := stripANSI(view.View().Content)

	assert.NotContains(t, rendered, ranked.Code(), "public browse must not advertise join codes")
	assert.NotContains(t, rendered, "CODE", "the code column is gone")
	assert.Contains(t, rendered, "Game")
	assert.Contains(t, rendered, "Seats")
	assert.Contains(t, rendered, "Poker")
	assert.Contains(t, rendered, "1/4", "seats taken over seats available")
	assert.Contains(t, rendered, "ranked", "ranked or casual has to be visible before joining")
	assert.Contains(t, rendered, "game: any", "the active filters are on screen")
	assert.Contains(t, rendered, ">", "the selection marker sits left of the table")
}

// A focused text field used to swallow every message, so ctrl+c did nothing and a
// player who had opened the code prompt could not quit without disconnecting.
func TestJoin_GlobalKeysSurviveTheCodePrompt(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	view := newJoinModel(t, m)

	pressJoin(t, view, "c")
	require.True(t, view.writingCode, "the code prompt has to be open for this to mean anything")

	_, cmd := view.Update(keyMsg("ctrl+c"))
	require.NotNil(t, cmd, "ctrl+c has to quit from a focused field too")
	assert.IsType(t, tea.QuitMsg{}, cmd())

	view.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	assert.Equal(t, 100, view.global.Width, "a resize still has to reach the layout")
	assert.Equal(t, 30, view.global.Height)
}

// Typing must still reach the field: the shared handler only claims ctrl+c, the
// theme and resizes, and a letter it claimed would be a letter never typed.
func TestJoin_TypingReachesTheCodeField(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	view := newJoinModel(t, m)

	pressJoin(t, view, "c")
	for _, key := range []string{"a", "b"} {
		pressJoin(t, view, key)
	}

	assert.Equal(t, "AB", view.textInput.Value(), "codes are upper-cased as they are typed")
}

func TestJoin_ViewSaysWhenNothingMatches(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	view := newJoinModel(t, m)

	rendered := stripANSI(view.View().Content)

	assert.Contains(t, rendered, "No tables match")
}

// stripANSI keeps assertions about text rather than the palette.
func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case !inEscape:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func manyTables(n int) []lobby.BrowseEntry {
	out := make([]lobby.BrowseEntry, 0, n)
	for i := range n {
		out = append(out, lobby.BrowseEntry{
			Code: fmt.Sprintf("T%03d", i), GameName: "Poker", Players: 1 + i%3, MaxPlayers: 4,
			Ranked: i%2 == 0, AvgElo: uint32(1500 + i),
		})
	}
	return out
}

// The browser is a full-screen view, so it has to fit the screen at every size the
// app claims to support: a fixed ten rows plus their chrome overran a stock 80x24
// terminal, and the frame handed the overflow to the terminal to wrap.
func TestJoin_ViewFitsTheTerminal(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		name string
		w, h int
	}{
		{"the declared minimum", styles.MinWidth, styles.MinHeight},
		{"a stock terminal", 80, 24},
		{"a tall terminal", 120, 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			m := newJoinModel(t, lobby.NewManager(context.Background(), nil))
			m.global.Width, m.global.Height = size.w, size.h
			m.entries = manyTables(30)
			m.cursor = 25 // deep in the list, so the window has to scroll

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.h, "taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "wider than the terminal")
			assert.Contains(t, stripANSI(out), ">Poker", "the cursor row is on screen")
			assert.Contains(t, stripANSI(out), "1525", "and it is the 26th table, not the first screenful")
		})
	}
}

// Init has to start both the cursor blink and the refresh loop: without the tick the
// list is a snapshot of whatever was open when the screen opened.
func TestJoin_InitStartsTheRefreshLoop(t *testing.T) {
	t.Parallel()
	view := newJoinModel(t, lobby.NewManager(context.Background(), nil))

	assert.NotNil(t, view.Init())
}

// The tick is what Update dispatches the refresh on, so it has to produce the message
// Update actually matches - a plain time.Time would be ignored and the list would freeze.
func TestJoin_RefreshTickProducesARefreshMsg(t *testing.T) {
	t.Parallel()
	view := newJoinModel(t, lobby.NewManager(context.Background(), nil))

	assert.Equal(t, refreshMsg{owner: view}, view.refreshTick()())
}

// Every join screen arms its own tick, and a tick already in flight when the player
// left lands on whatever screen is up next. esc then f inside the window used to hand
// the old tick to the new screen, which re-armed it: two refresh chains, then three.
func TestJoin_AStaleRefreshIsDroppedWithoutRearming(t *testing.T) {
	t.Parallel()
	m := lobby.NewManager(context.Background(), nil)
	left, current := newJoinModel(t, m), newJoinModel(t, m)

	_, cmd := current.Update(refreshMsg{owner: left})

	assert.Nil(t, cmd, "the old screen's tick must not start a second chain here")
}

func TestJoin_JoinByCode(t *testing.T) {
	t.Parallel()

	t.Run("an empty code is not a join attempt", func(t *testing.T) {
		t.Parallel()
		view := newJoinModel(t, lobby.NewManager(context.Background(), nil))

		_, cmd := view.joinByCode("")

		assert.Nil(t, cmd)
		assert.NoError(t, view.err, "the player typed nothing, which is not an error")
	})

	// The table may have filled or started while the code was being typed, so the
	// refusal has to be on screen next to a list that is current again.
	t.Run("an unknown code says so and re-reads the list", func(t *testing.T) {
		t.Parallel()
		m := lobby.NewManager(context.Background(), nil)
		view := newJoinModel(t, m)
		openPublicTable(t, m, "host", testutil.UID(1), testGameName)

		_, cmd := view.joinByCode("NOSUCH12")

		assert.Nil(t, cmd, "there is nowhere to navigate")
		require.Error(t, view.err)
		assert.Len(t, view.entries, 1, "the list was refreshed, not left stale")
		assert.Contains(t, stripANSI(view.View().Content), "Error:")
	})

	t.Run("a good code seats the player and opens the lobby", func(t *testing.T) {
		t.Parallel()
		m := lobby.NewManager(context.Background(), nil)
		table := openPublicTable(t, m, "host", testutil.UID(1), testGameName)
		view := newJoinModel(t, m)

		_, cmd := view.joinByCode(table.Code())

		require.NotNil(t, cmd)
		change, ok := cmd().(router.ChangeViewMsg)
		require.True(t, ok)
		assert.Equal(t, router.RouteLobby, change.ViewName)
		assert.Same(t, table, change.Context, "the lobby view is handed the table that was joined")
		assert.NoError(t, view.err)
	})
}

// Enter and space both join the highlighted row; the browser is useless if the row the
// cursor is on is not the one it acts on.
func TestJoin_SelectingARowJoinsIt(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"enter", "space"} {
		t.Run("with "+key, func(t *testing.T) {
			t.Parallel()
			m := lobby.NewManager(context.Background(), nil)
			openPublicTable(t, m, "host", testutil.UID(1), testGameName)
			view := newJoinModel(t, m)
			require.Len(t, view.entries, 1)

			_, cmd := view.Update(keyMsg(key))

			require.NotNil(t, cmd)
			change, ok := cmd().(router.ChangeViewMsg)
			require.True(t, ok)
			assert.Equal(t, router.RouteLobby, change.ViewName)
		})
	}
}

// The code prompt is a mode, and every way in and out of it has to work: a player who
// can open it but not cancel it is stuck on a field with no table in sight.
func TestJoin_CodeEntryFlow(t *testing.T) {
	t.Parallel()

	t.Run("escape cancels without joining", func(t *testing.T) {
		t.Parallel()
		view := newJoinModel(t, lobby.NewManager(context.Background(), nil))
		pressJoin(t, view, "c")
		require.True(t, view.writingCode)

		pressJoin(t, view, "a")
		pressJoin(t, view, "esc")

		assert.False(t, view.writingCode)
		assert.NoError(t, view.err, "cancelling is not a failed join")
	})

	t.Run("backspace corrects a typo", func(t *testing.T) {
		t.Parallel()
		view := newJoinModel(t, lobby.NewManager(context.Background(), nil))
		pressJoin(t, view, "c")
		for _, key := range []string{"a", "b", "backspace"} {
			pressJoin(t, view, key)
		}

		assert.Equal(t, "A", view.textInput.Value())
	})

	t.Run("enter submits what was typed", func(t *testing.T) {
		t.Parallel()
		m := lobby.NewManager(context.Background(), nil)
		table := openPublicTable(t, m, "host", testutil.UID(1), testGameName)
		view := newJoinModel(t, m)

		pressJoin(t, view, "c")
		for _, r := range strings.ToLower(table.Code()) {
			pressJoin(t, view, string(r))
		}
		_, cmd := view.Update(keyMsg("enter"))

		require.NotNil(t, cmd, "a typed code is upper-cased on the way to the manager")
		change, ok := cmd().(router.ChangeViewMsg)
		require.True(t, ok)
		assert.Equal(t, router.RouteLobby, change.ViewName)
	})
}

// The cursor blink is not a key press, and it has to reach the field it belongs to -
// otherwise the caret stops moving the moment the prompt opens.
func TestJoin_NonKeyMessagesReachTheFocusedField(t *testing.T) {
	t.Parallel()
	view := newJoinModel(t, lobby.NewManager(context.Background(), nil))

	_, cmd := view.Update(struct{ tea.Msg }{})
	assert.Nil(t, cmd, "while browsing there is nothing to forward it to")

	pressJoin(t, view, "c")
	updated, _ := view.Update(struct{ tea.Msg }{})
	_, ok := updated.(*joinModel)
	assert.True(t, ok, "a focused field still owns the message")
}

// The status line is the only thing telling the player why the list looks the way it
// does, so every filter value has to have a name on it.
func TestJoin_FilterLineNamesEveryFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter lobby.BrowseFilter
		want   string
	}{
		{name: "the defaults", filter: lobby.BrowseFilter{OnlyWithRoom: true}, want: "game: any   mode: any   showing: with seats"},
		{name: "ranked only", filter: lobby.BrowseFilter{Mode: lobby.BrowseRanked}, want: "game: any   mode: ranked   showing: all tables"},
		{name: "casual only", filter: lobby.BrowseFilter{Mode: lobby.BrowseCasual}, want: "game: any   mode: casual   showing: all tables"},
		{name: "pinned to a game", filter: lobby.BrowseFilter{GameName: "Poker"}, want: "game: Poker   mode: any   showing: all tables"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			view := newJoinModel(t, lobby.NewManager(context.Background(), nil))
			view.filter = tt.filter

			assert.Equal(t, tt.want, stripANSI(view.filterLine()))
		})
	}
}
