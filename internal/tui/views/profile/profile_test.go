package profile

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	"uuid"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errQuery = errors.New("query failed")

func loaded(t *testing.T, msg profileLoadedMsg) *model {
	t.Helper()
	global := router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 40,
		GameRegistry: catalog.NewRegistry()}
	updated, _ := New(global).Update(msg)
	m, ok := updated.(*model)
	require.True(t, ok)
	return m
}

func alice() *db.User {
	return &db.User{ID: testutil.UID(1),
		Username: "alice",
		Rankings: []db.Ranking{{Elo: 1600, Game: db.Game{Name: "Poker"}}},
	}
}

// The two queries used to share one error field, so a match history that failed
// threw away a profile that had loaded and the screen said the whole profile was
// unreadable. The half that loaded is still worth showing.
func TestUpdate_AFailedHistoryKeepsTheProfile(t *testing.T) {
	t.Parallel()
	m := loaded(t, profileLoadedMsg{user: alice(), historyErr: errQuery})

	require.NotNil(t, m.userProfile, "the profile query succeeded")
	out := tuitest.StripANSI(m.renderContent(20))

	assert.Contains(t, out, "Profile for: alice")
	assert.Contains(t, out, "Poker", "the rankings came back with the profile")
	assert.Contains(t, out, "1600")
	assert.Contains(t, out, "Unable to load match history.", "and the half that failed says so")
	assert.NotContains(t, out, "Unable to load profile")
}

// A profile that could not be read has nothing to draw, so that failure still
// takes the whole screen.
func TestUpdate_AFailedProfileIsStillAnErrorScreen(t *testing.T) {
	t.Parallel()
	m := loaded(t, profileLoadedMsg{err: errQuery})

	assert.Contains(t, m.renderContent(20), "Unable to load profile")
}

func TestRenderContent_SaysWhenThereAreNoMatchesRatherThanAnError(t *testing.T) {
	t.Parallel()
	m := loaded(t, profileLoadedMsg{user: alice()})

	out := tuitest.StripANSI(m.renderContent(20))
	assert.Contains(t, out, "No matches for this filter.")
	assert.NotContains(t, out, "Unable to load match history.")
}

func TestFilteredHistory_WinsAndGame(t *testing.T) {
	t.Parallel()
	history := []db.MatchParticipant{
		{Placement: 1, Match: db.Match{Game: db.Game{Name: "Uno"}, Ranked: true}},
		{Placement: 2, Match: db.Match{Game: db.Game{Name: "Uno"}, Ranked: true}},
		{Placement: 1, Match: db.Match{Game: db.Game{Name: "Poker"}, Ranked: true}},
	}
	m := loaded(t, profileLoadedMsg{user: alice(), history: history})

	m.gameFilterIdx = slices.Index(m.gameFilters, "Uno")
	require.NotEqual(t, -1, m.gameFilterIdx)
	m.resultIdx = slices.Index(m.resultFilters, filterWins)

	filtered := m.filteredHistory()
	require.Len(t, filtered, 1)
	assert.Equal(t, 1, filtered[0].Placement)
	assert.Equal(t, "Uno", filtered[0].Match.Game.Name)
}

func TestRenderContent_FilterDoesNotResizeLayout(t *testing.T) {
	t.Parallel()
	history := []db.MatchParticipant{
		{Placement: 1, EloDelta: 12, Match: db.Match{Game: db.Game{Name: "Uno"}, Ranked: true}},
	}
	m := loaded(t, profileLoadedMsg{user: alice(), history: history})

	base := tuitest.StripANSI(m.renderContent(20))
	m.gameFilterIdx = slices.Index(m.gameFilters, "Crazy Eights")
	require.NotEqual(t, -1, m.gameFilterIdx)
	m.resultIdx = slices.Index(m.resultFilters, filterLosses)
	filtered := tuitest.StripANSI(m.renderContent(20))

	assert.Equal(t, lg.Width(base), lg.Width(filtered),
		"cycling game/result filters must not change the profile width")
}

// The profile is a full-screen view, so it has to fit the screen at every size the
// app claims to support - a view taller than the terminal is handed to the terminal
// to wrap, and one wrapped row shifts every row under it.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	user := alice()
	user.Rankings = make([]db.Ranking, 0, 5)
	for _, g := range []string{"Poker", "Hearts", "Uno", "Gin Rummy", "Crazy Eights"} {
		user.Rankings = append(user.Rankings, db.Ranking{Elo: 1600, Game: db.Game{Name: g}})
	}
	history := make([]db.MatchParticipant, 0, 50)
	for i := range 50 {
		history = append(history, db.MatchParticipant{
			Placement: (i % 4) + 1,
			Match:     db.Match{Game: db.Game{Name: "Poker"}, Ranked: true},
		})
	}

	for _, size := range tuitest.FitSizes {
		t.Run(size.Name, func(t *testing.T) {
			t.Parallel()
			global := router.GlobalContext{
				Theme: styles.NewTheme(true), Width: size.Width, Height: size.Height,
				// The delete confirmation names the account it would anonymise, so the
				// widest id is what the line has to fit.
				User: &db.User{ID: testutil.UID(99), Username: "alice"},
			}
			updated, _ := New(global).Update(profileLoadedMsg{user: user, history: history})
			m, ok := updated.(*model)
			require.True(t, ok)

			// Every state the screen can be in, not only the table: the erasure
			// confirmation is the tallest of them and would be the one to overflow.
			states := map[string]*model{"tables": m}
			refused := *m
			refused.notice = "Leave your table before deleting your account."
			states["refused"] = &refused
			confirming := *m
			confirming.phase = deleteConfirming
			states["confirming"] = &confirming
			typo := confirming
			typo.typed = "DELETEDEL"
			typo.notice = "Type DELETE exactly, then press enter."
			states["confirming with a notice"] = &typo
			running := *m
			running.phase = deleteRunning
			states["deleting"] = &running
			done := *m
			done.phase = deleteDone
			states["deleted"] = &done

			for name, state := range states {
				out := state.View().Content
				assert.LessOrEqual(t, lg.Height(out), size.Height, "%s is taller than the terminal", name)
				assert.LessOrEqual(t, lg.Width(out), size.Width, "%s is wider than the terminal", name)
			}
		})
	}
}

// fakeUsers stands in for the repository. Only the two profile queries are reachable
// from this view; the embedded interface turns any other call into a loud nil panic
// rather than a quietly passing test.
type fakeUsers struct {
	db.UserRepository
	profile       func(ctx context.Context, userID uuid.UUID) (*db.User, error)
	history       func(ctx context.Context, userID uuid.UUID, limit int) ([]db.MatchParticipant, error)
	deleteAccount func(ctx context.Context, userID uuid.UUID) error
}

func (f fakeUsers) DeleteAccount(ctx context.Context, userID uuid.UUID) error {
	return f.deleteAccount(ctx, userID)
}

func (f fakeUsers) UserProfile(ctx context.Context, userID uuid.UUID) (*db.User, error) {
	return f.profile(ctx, userID)
}

func (f fakeUsers) UserMatchHistory(ctx context.Context, userID uuid.UUID, limit int) ([]db.MatchParticipant, error) {
	return f.history(ctx, userID, limit)
}

func TestLoadProfile(t *testing.T) {
	t.Parallel()

	t.Run("both queries land in one message", func(t *testing.T) {
		t.Parallel()
		var gotLimit int
		repo := fakeUsers{
			profile: func(_ context.Context, id uuid.UUID) (*db.User, error) {
				assert.Equal(t, testutil.UID(1), id)
				return alice(), nil
			},
			history: func(_ context.Context, id uuid.UUID, limit int) ([]db.MatchParticipant, error) {
				assert.Equal(t, testutil.UID(1), id)
				gotLimit = limit
				return []db.MatchParticipant{{Placement: 1}}, nil
			},
		}

		msg, ok := loadProfile(t.Context(), repo, testutil.UID(1))().(profileLoadedMsg)
		require.True(t, ok)

		assert.Equal(t, historyFetchLimit, gotLimit)
		require.NotNil(t, msg.user)
		assert.Len(t, msg.history, 1)
		assert.NoError(t, msg.err)
		assert.NoError(t, msg.historyErr)
	})

	// There is nothing to show a player whose profile row could not be read, so the
	// history query is not worth issuing - and its result would have nowhere to go.
	t.Run("a failed profile short-circuits the history query", func(t *testing.T) {
		t.Parallel()
		repo := fakeUsers{
			profile: func(context.Context, uuid.UUID) (*db.User, error) { return nil, errQuery },
			history: func(context.Context, uuid.UUID, int) ([]db.MatchParticipant, error) {
				t.Error("history must not be queried once the profile failed")
				return nil, nil
			},
		}

		msg, ok := loadProfile(t.Context(), repo, testutil.UID(1))().(profileLoadedMsg)
		require.True(t, ok)

		require.ErrorIs(t, msg.err, errQuery)
		assert.Nil(t, msg.user)
	})

	// The two failures stay apart: a profile that loaded is still worth showing.
	t.Run("a failed history keeps the profile", func(t *testing.T) {
		t.Parallel()
		repo := fakeUsers{
			profile: func(context.Context, uuid.UUID) (*db.User, error) { return alice(), nil },
			history: func(context.Context, uuid.UUID, int) ([]db.MatchParticipant, error) {
				return nil, errQuery
			},
		}

		msg, ok := loadProfile(t.Context(), repo, testutil.UID(1))().(profileLoadedMsg)
		require.True(t, ok)

		require.NoError(t, msg.err)
		require.ErrorIs(t, msg.historyErr, errQuery)
		require.NotNil(t, msg.user)
	})
}

func TestInit(t *testing.T) {
	t.Parallel()

	// The view is constructed before auth has resolved on some paths; querying for
	// user zero would be a round trip that can only come back empty.
	t.Run("no user yet means no query", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, New(router.GlobalContext{}).Init())
	})

	t.Run("a signed-in user loads their own profile", func(t *testing.T) {
		t.Parallel()
		var gotID uuid.UUID
		repo := fakeUsers{
			profile: func(_ context.Context, id uuid.UUID) (*db.User, error) {
				gotID = id
				return alice(), nil
			},
			history: func(context.Context, uuid.UUID, int) ([]db.MatchParticipant, error) { return nil, nil },
		}
		user := alice()

		cmd := New(router.GlobalContext{User: user, UserRepository: repo}).Init()
		require.NotNil(t, cmd)
		_, ok := cmd().(profileLoadedMsg)

		require.True(t, ok)
		assert.Equal(t, user.ID, gotID, "the profile shown is always the session's own")
	})
}

// The filter keys cycle and wrap; the layout is fixed-width precisely so they can.
func TestUpdate_CyclesFilters(t *testing.T) {
	t.Parallel()

	m := loaded(t, profileLoadedMsg{user: alice()})
	require.Equal(t, 0, m.gameFilterIdx)

	for i := 1; i <= len(m.gameFilters); i++ {
		next, cmd := m.Update(tuitest.Key("g"))
		m = next.(*model)
		assert.Nil(t, cmd, "cycling a filter is local; it must not re-query")
		assert.Equal(t, i%len(m.gameFilters), m.gameFilterIdx)
	}

	for i := 1; i <= len(m.resultFilters); i++ {
		next, _ := m.Update(tuitest.Key("r"))
		m = next.(*model)
		assert.Equal(t, i%len(m.resultFilters), m.resultIdx)
	}
}

func TestUpdate_KeysTheViewDoesNotOwn(t *testing.T) {
	t.Parallel()

	t.Run("navigation reaches the shared handler", func(t *testing.T) {
		t.Parallel()
		m := loaded(t, profileLoadedMsg{user: alice()})

		_, cmd := m.Update(tuitest.Key("t"))

		require.NotNil(t, cmd)
		msg, ok := cmd().(router.ChangeViewMsg)
		require.True(t, ok)
		assert.Equal(t, router.RouteLeaderboard, msg.ViewName)
	})

	t.Run("an unbound key does nothing", func(t *testing.T) {
		t.Parallel()
		m := loaded(t, profileLoadedMsg{user: alice()})

		next, cmd := m.Update(tuitest.Key("z"))

		assert.Nil(t, cmd)
		assert.Zero(t, next.(*model).gameFilterIdx)
	})

	// A resize has to land on the view's own copy of the context, or the profile
	// keeps rendering at the size it was built with.
	t.Run("a resize updates this view's context", func(t *testing.T) {
		t.Parallel()
		m := loaded(t, profileLoadedMsg{user: alice()})

		next, cmd := m.Update(tea.WindowSizeMsg{Width: 130, Height: 60})

		assert.Nil(t, cmd)
		assert.Equal(t, 130, next.(*model).global.Width)
		assert.Equal(t, 60, next.(*model).global.Height)
	})
}

// A truncated table has to spend a row on saying so rather than appending one past
// its budget: an extra line here pushes the whole frame past the terminal.
func TestRankingRows_TruncatesInsideItsBudget(t *testing.T) {
	t.Parallel()

	user := alice()
	user.Rankings = nil
	for _, g := range []string{"Poker", "Hearts", "Uno", "Gin Rummy", "Crazy Eights"} {
		user.Rankings = append(user.Rankings, db.Ranking{Elo: 1500, Game: db.Game{Name: g}})
	}
	m := loaded(t, profileLoadedMsg{user: user})

	t.Run("everything fits", func(t *testing.T) {
		t.Parallel()
		rows := m.rankingRows(5)
		assert.Len(t, rows, 6, "one header plus every ranking")
		assert.NotContains(t, strings.Join(rows, "\n"), "... and more")
	})

	t.Run("more than fits", func(t *testing.T) {
		t.Parallel()
		rows := m.rankingRows(3)
		assert.Len(t, rows, 4, "one header plus two rankings plus the truncation line")
		assert.Equal(t, "... and more", rows[len(rows)-1])
		assert.Contains(t, tuitest.StripANSI(rows[1]), "Poker")
	})

	// A player who has never played still gets a table, not a bare header.
	t.Run("nothing to show says so", func(t *testing.T) {
		t.Parallel()
		empty := loaded(t, profileLoadedMsg{user: &db.User{Username: "newbie"}})
		rows := empty.rankingRows(5)
		require.Len(t, rows, 2)
		assert.Contains(t, rows[1], "No games yet.")
	})
}

func TestHistoryRows_TruncatesInsideItsBudget(t *testing.T) {
	t.Parallel()

	history := make([]db.MatchParticipant, 0, 10)
	for i := range 10 {
		history = append(history, db.MatchParticipant{
			Placement: i + 1, Match: db.Match{Game: db.Game{Name: "Poker"}, Ranked: true},
		})
	}
	m := loaded(t, profileLoadedMsg{user: alice(), history: history})

	rows := m.historyRows(4)

	assert.Len(t, rows, 5, "one header plus three matches plus the truncation line")
	assert.Equal(t, "... and more", rows[len(rows)-1])
	assert.Contains(t, tuitest.StripANSI(rows[1]), "1st place")
	assert.Contains(t, tuitest.StripANSI(rows[3]), "3rd place")
}

func TestFilteredHistory(t *testing.T) {
	t.Parallel()

	history := []db.MatchParticipant{
		{Placement: 1, Match: db.Match{Game: db.Game{Name: "Uno"}, Ranked: true}},
		{Placement: 2, Match: db.Match{Game: db.Game{Name: "Uno"}, Ranked: true}},
		{Placement: 4, Match: db.Match{Game: db.Game{Name: "Poker"}}},
	}

	tests := []struct {
		name   string
		game   string
		result string
		want   int
	}{
		{name: "no filters keeps everything", game: filterAllGames, result: filterAllResults, want: 3},
		{name: "wins only", game: filterAllGames, result: filterWins, want: 1},
		// A loss is everything that is not first place, including a fourth-place
		// finish - not only second.
		{name: "losses only", game: filterAllGames, result: filterLosses, want: 2},
		{name: "one game", game: "Uno", result: filterAllResults, want: 2},
		{name: "both filters at once", game: "Uno", result: filterLosses, want: 1},
		{name: "a game never played", game: "Hearts", result: filterAllResults, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := loaded(t, profileLoadedMsg{user: alice(), history: history})
			m.gameFilterIdx = slices.Index(m.gameFilters, tt.game)
			require.NotEqual(t, -1, m.gameFilterIdx)
			m.resultIdx = slices.Index(m.resultFilters, tt.result)
			require.NotEqual(t, -1, m.resultIdx)

			assert.Len(t, m.filteredHistory(), tt.want)
		})
	}
}

// An unranked match moved no Elo, so printing "Elo +0" for it would read as a
// ranked draw rather than as a game that never counted.
func TestResultPlain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		h    db.MatchParticipant
		want string
	}{
		{name: "casual", h: db.MatchParticipant{EloDelta: 12}, want: "casual game"},
		{name: "gain", h: db.MatchParticipant{EloDelta: 12, Match: db.Match{Ranked: true}}, want: "Elo +12"},
		{name: "loss", h: db.MatchParticipant{EloDelta: -8, Match: db.Match{Ranked: true}}, want: "Elo -8"},
		{name: "a ranked draw", h: db.MatchParticipant{Match: db.Match{Ranked: true}}, want: "Elo +0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resultPlain(tt.h))
		})
	}
}

// Beyond third the ordinal suffix is not worth a table, so the placement falls back
// to a number - it still has to fit the fixed cell.
func TestPlacementPlain(t *testing.T) {
	t.Parallel()

	for placement, want := range map[int]string{
		1: "1st place", 2: "2nd place", 3: "3rd place", 4: "4 place", 11: "11 place",
	} {
		assert.Equal(t, want, placementPlain(placement))
		assert.LessOrEqual(t, len(want), colPlace, "the placement cell is fixed-width")
	}
}

func typeWord(t *testing.T, m *model, word string) *model {
	t.Helper()
	for _, r := range word {
		next, cmd := m.Update(tuitest.Key(string(r)))
		assert.Nil(t, cmd, "typing into the confirmation must not issue a command")
		var ok bool
		m, ok = next.(*model)
		require.True(t, ok)
	}
	return m
}

// deletingModel is a profile sitting on a loaded account, with a repository whose
// DeleteAccount reports err.
func deletingModel(t *testing.T, err error) (*model, *int) {
	t.Helper()
	calls := 0
	repo := fakeUsers{deleteAccount: func(_ context.Context, id uuid.UUID) error {
		calls++
		assert.Equal(t, testutil.UID(1), id, "a session may only erase its own account")
		return err
	}}
	global := router.GlobalContext{
		Theme: styles.NewTheme(true), Width: 100, Height: 40,
		User: alice(), UserRepository: repo,
	}
	updated, _ := New(global).Update(profileLoadedMsg{user: alice()})
	m, ok := updated.(*model)
	require.True(t, ok)
	return m, &calls
}

// Erasure is irreversible, so it takes a deliberate word rather than a keystroke: x
// opens the confirmation, only the exact word arms it, and only then does the account
// go and the session end.
func TestUpdate_DeleteAccountFlow(t *testing.T) {
	t.Parallel()

	t.Run("x opens the confirmation and explains what it does", func(t *testing.T) {
		t.Parallel()
		m, calls := deletingModel(t, nil)

		next, cmd := m.Update(tuitest.Key("x"))
		m, ok := next.(*model)
		require.True(t, ok)

		assert.Nil(t, cmd, "opening the confirmation asks the database nothing")
		assert.Equal(t, deleteConfirming, m.phase)
		out := tuitest.StripANSI(m.renderContent(20))
		assert.Contains(t, out, "SSH keys removed")
		assert.Contains(t, out, "ratings removed")
		assert.Contains(t, out, db.AnonymisedUsername(alice().ID), "the name past matches will show")
		assert.Contains(t, out, "cannot be undone")
		assert.Contains(t, out, deleteConfirmWord)
		assert.Zero(t, *calls)
	})

	// While the confirmation is open the filter keys are letters of the word, not
	// filters - "DELETE" contains E, and a modal that leaked keys would cycle them.
	t.Run("the confirmation swallows the view's other keys", func(t *testing.T) {
		t.Parallel()
		m, _ := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), "gr")

		assert.Equal(t, "gr", m.typed)
		assert.Zero(t, m.gameFilterIdx, "a typed letter must not cycle the game filter")
		assert.Zero(t, m.resultIdx)
	})

	t.Run("the wrong word does not delete anything", func(t *testing.T) {
		t.Parallel()
		m, calls := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), "delete")

		after, cmd := m.Update(tuitest.Key("enter"))
		m, ok := after.(*model)
		require.True(t, ok)

		assert.Nil(t, cmd, "a mistyped confirmation must not reach the repository")
		assert.Zero(t, *calls)
		assert.Equal(t, deleteConfirming, m.phase, "the player stays in the confirmation")
		assert.Contains(t, m.notice, deleteConfirmWord)
	})

	t.Run("backspace corrects a typo", func(t *testing.T) {
		t.Parallel()
		m, _ := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), "DELETX")

		after, _ := m.Update(tuitest.Key("backspace"))
		m, ok := after.(*model)
		require.True(t, ok)
		m = typeWord(t, m, "E")

		assert.Equal(t, deleteConfirmWord, m.typed)
	})

	t.Run("the exact word deletes and ends the session", func(t *testing.T) {
		t.Parallel()
		m, calls := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), deleteConfirmWord)

		after, cmd := m.Update(tuitest.Key("enter"))
		require.NotNil(t, cmd, "the confirmed word has to issue the delete")
		m, ok := after.(*model)
		require.True(t, ok)
		assert.Equal(t, deleteRunning, m.phase, "running until the query answers")

		msg, ok := cmd().(accountDeletedMsg)
		require.True(t, ok)
		require.NoError(t, msg.err)
		assert.Equal(t, 1, *calls)

		done, quit := m.Update(msg)
		m, ok = done.(*model)
		require.True(t, ok)
		assert.Equal(t, deleteDone, m.phase)
		require.NotNil(t, quit)
		assert.IsType(t, tea.QuitMsg{}, quit(), "the session ends through the normal quit path")
		assert.Contains(t, tuitest.StripANSI(m.renderContent(20)), "deleted")
	})

	// A failed delete leaves the player where they were, told so, rather than quitting
	// the session on a deletion that did not happen.
	t.Run("a failed delete keeps the session", func(t *testing.T) {
		t.Parallel()
		m, _ := deletingModel(t, errQuery)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), deleteConfirmWord)
		after, cmd := m.Update(tuitest.Key("enter"))
		require.NotNil(t, cmd)

		done, quit := after.(*model).Update(cmd())
		m, ok := done.(*model)
		require.True(t, ok)

		assert.Nil(t, quit, "a failed erasure must not end the session")
		assert.Equal(t, deleteConfirming, m.phase)
		assert.Contains(t, m.notice, "Could not delete")
		assert.Empty(t, m.typed, "the word is retyped rather than resubmitted by accident")
	})

	// Once the delete is issued the player cannot back out of it, navigate away and
	// keep playing on an account that is being erased, or issue it a second time.
	t.Run("the running delete swallows every key until it answers", func(t *testing.T) {
		t.Parallel()
		m, calls := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), deleteConfirmWord)
		after, cmd := m.Update(tuitest.Key("enter"))
		require.NotNil(t, cmd)
		m = after.(*model)

		for _, k := range []tea.KeyPressMsg{tuitest.Key("esc"), tuitest.Key("q"), tuitest.Key("t"), tuitest.Key("enter")} {
			after, swallowed := m.Update(k)
			m = after.(*model)
			assert.Nil(t, swallowed, "%q must not act while the delete runs", k.String())
		}
		assert.Equal(t, deleteRunning, m.phase)
		assert.Zero(t, *calls, "nothing issued a second delete")

		done, quit := m.Update(cmd())
		assert.Equal(t, deleteDone, done.(*model).phase)
		require.NotNil(t, quit)
	})

	t.Run("esc cancels", func(t *testing.T) {
		t.Parallel()
		m, calls := deletingModel(t, nil)
		next, _ := m.Update(tuitest.Key("x"))
		m = typeWord(t, next.(*model), deleteConfirmWord)

		after, cmd := m.Update(tuitest.Key("esc"))
		m, ok := after.(*model)
		require.True(t, ok)

		assert.Nil(t, cmd, "esc out of the confirmation is not navigation")
		assert.Equal(t, deleteIdle, m.phase)
		assert.Empty(t, m.typed, "a cancelled confirmation does not remember the word")
		assert.Zero(t, *calls)
		assert.Contains(t, tuitest.StripANSI(m.renderContent(20)), "Profile for: alice")
	})
}

// A seat is live state the lobby and engine hold under this player ID; erasing the
// account under it would rename a player mid-hand.
func TestUpdate_DeleteRefusedWhileSeated(t *testing.T) {
	t.Parallel()

	m, calls := deletingModel(t, nil)
	manager := lobby.NewManager(t.Context(), nil)
	_, err := manager.CreateLobby(views.SessionPlayer(m.global), lobby.WithCardGame(catalog.All[0].Name))
	require.NoError(t, err)
	m.global.LobbyManager = manager

	next, cmd := m.Update(tuitest.Key("x"))
	m, ok := next.(*model)
	require.True(t, ok)

	assert.Nil(t, cmd)
	assert.Equal(t, deleteIdle, m.phase, "the confirmation must not even open")
	assert.Zero(t, *calls)
	out := tuitest.StripANSI(m.renderContent(20))
	assert.Contains(t, out, "Leave your table")
	assert.Contains(t, out, "Profile for: alice", "the refusal is a line, not a screen")

	// The refusal clears on the next key, so it cannot outlive the seat it describes.
	after, _ := m.Update(tuitest.Key("g"))
	assert.Empty(t, after.(*model).notice)
}

// The footer advertises the key, so the key has to be there - and a footer entry
// nothing binds is a shortcut that silently does nothing.
func TestView_AdvertisesTheDeleteKey(t *testing.T) {
	t.Parallel()
	m, _ := deletingModel(t, nil)

	assert.Contains(t, tuitest.StripANSI(m.View().Content), "x - Delete account")
}

// At the declared minimum the full warning is taller than the rows the frame has, and
// the layout clips rather than shrinking - so what disappears is the bottom, which is
// the line the player has to type into. The short form says the same things in four.
func TestRenderConfirm_KeepsThePromptAtTheMinimumSize(t *testing.T) {
	t.Parallel()

	global := router.GlobalContext{
		Theme: styles.NewTheme(true), Width: styles.MinWidth, Height: styles.MinHeight,
		User: &db.User{ID: testutil.UID(99)},
	}
	m, ok := New(global).(*model)
	require.True(t, ok)
	m.phase = deleteConfirming

	out := tuitest.StripANSI(m.View().Content)

	assert.Contains(t, out, deleteConfirmWord, "the word to type must be on screen")
	assert.Contains(t, out, "esc to cancel", "so must the way out")
	assert.Contains(t, out, "> ", "and the line being typed into")
	assert.Contains(t, out, db.AnonymisedUsername(testutil.UID(99)), "and the name that replaces theirs")
}
