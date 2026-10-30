package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	crazyview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/tui/views/home"
	lobbyview "github.com/Pieczasz/tty-cards/internal/tui/views/lobby"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seatGlobal is seat n's session at the terminal size the bots request.
func seatGlobal(t *testing.T, mgr *lobby.Manager, n int) router.GlobalContext {
	t.Helper()
	return router.GlobalContext{
		Player:       lobby.NewPlayer(&db.User{ID: testutil.UID(n), Username: fmt.Sprintf("load_%04d", n)}),
		LobbyManager: mgr, Games: catalog.Modules(catalog.Games),
		Theme: styles.NewTheme(true), Width: termWidth, Height: termHeight,
	}
}

// seatView is seat n's Crazy Eights view, exactly as the router builds it. The
// caller closes it: a table has only so many subscriber slots.
func seatView(t *testing.T, engine *game.Engine, n int) tea.Model {
	t.Helper()
	return crazyview.New(seatGlobal(t, lobby.NewManager(t.Context(), nil, catalog.NewRegistry()), n), game.Bind(engine, testutil.SeatID(n)), "crazy_eights")
}

func closeView(m tea.Model) { m.(router.Closer).Close() }

func renderSeat(t *testing.T, engine *game.Engine, seat string) string {
	t.Helper()
	n := slices.IndexFunc(engine.Snapshot().Players, func(p game.PlayerSnapshot) bool { return p.ID == seat })
	require.GreaterOrEqual(t, n, 0, "no such seat")
	m := seatView(t, engine, n+1)
	defer closeView(m)
	return m.View().Content
}

func frameLines(content string) []string {
	return strings.Split(ansi.Strip(content), "\n")
}

// keyMsgs is keys, as a bot types them, as the messages Bubble Tea makes of them.
func keyMsgs(keys string) []tea.KeyPressMsg {
	names := map[string]string{keyUp: "up", keyDown: "down", keyRight: "right", keyLeft: "left", keyEnter: "enter", keyEsc: "esc"}
	var msgs []tea.KeyPressMsg
	for len(keys) > 0 {
		n := 1
		if strings.HasPrefix(keys, "\x1b[") {
			n = 3
		}
		name, ok := names[keys[:n]]
		if !ok {
			name = keys[:n]
		}
		msgs = append(msgs, tuitest.Key(name))
		keys = keys[n:]
	}
	return msgs
}

func press(m tea.Model, keys string) tea.Model {
	for _, k := range keyMsgs(keys) {
		m, _ = m.Update(k)
	}
	return m
}

// Each screen a bot walks through, rendered by the real view, is told apart - and
// the menus' shared footer does not make every one of them read as home.
func TestClassify_RealScreens(t *testing.T) {
	t.Parallel()
	mgr := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
	g := seatGlobal(t, mgr, 1)
	l, err := mgr.CreateLobby(g.Player, lobby.WithCardGame("crazy_eights"))
	require.NoError(t, err)

	engine := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("a", "b"))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)
	over := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("a", "b"))
	require.NoError(t, over.Start())
	t.Cleanup(over.Close)
	over.RemovePlayer(testutil.SeatID(2))

	tests := []struct {
		name  string
		model tea.Model
		keys  string
		want  view
	}{
		{"home", home.New(g), "", viewHome},
		{"create", lobbyview.NewCreate(g), "", viewCreate},
		{"join browser", lobbyview.NewJoin(g), "", viewJoin},
		{"join, typing a code", lobbyview.NewJoin(g), "c", viewJoin},
		{"lobby", lobbyview.New(g, l), "", viewLobby},
		{"lobby, leave prompt", lobbyview.New(g, l), "x", viewLobbyLeave},
		{"game", seatView(t, engine, 1), "", viewGame},
		{"game, forfeit prompt", seatView(t, engine, 1), keyEsc, viewForfeit},
		{"game over", seatView(t, over, 1), "", viewGameOver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := press(tt.model, tt.keys)
			if c, ok := m.(router.Closer); ok {
				t.Cleanup(c.Close)
			}
			assert.Equal(t, tt.want, classify(frameLines(m.View().Content)))
		})
	}
}

var rankLabels = map[deck.Rank]string{
	deck.Ace: "A", deck.Jack: "J", deck.Queen: "Q", deck.King: "K",
}

func label(c deck.Card) card {
	rank, ok := rankLabels[c.Rank]
	if !ok {
		rank = strconv.Itoa(int(c.Rank))
	}
	suits := map[deck.Suit]string{deck.Spades: "♠", deck.Hearts: "♥", deck.Diamonds: "♦", deck.Clubs: "♣"}
	return card{rank: rank, suit: suits[c.Suit]}
}

// truth is what the engine says seat's frame should show.
func truth(engine *game.Engine, seat string) (hand []card, top string, suit string, myTurn bool) {
	engine.WithState(func(s *game.State) {
		for _, p := range s.Players {
			if p.ID == seat {
				for _, c := range p.Cards {
					hand = append(hand, label(c))
				}
			}
		}
		cards := s.Discard.Cards()
		top = label(cards[len(cards)-1]).rank
		suit = label(deck.Card{Suit: s.Extra.(*logic.State).CurrentSuit}).suit
		myTurn = s.Players[s.CurrentTurn].ID == seat
	})
	return hand, top, suit, myTurn
}

func sortedCards(cs []card) []card {
	return slices.SortedFunc(slices.Values(cs), func(a, b card) int {
		return strings.Compare(a.rank+a.suit, b.rank+b.suit)
	})
}

// The frame is the bot's only source of truth, so what it reads off one has to be
// what the engine holds - every card, including a hand long enough to wrap the strip
// onto a second row, the discard's rank and the suit in play.
func TestParseCrazy_ReadsWhatTheEngineHolds(t *testing.T) {
	t.Parallel()
	engine := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("alice", "bob"))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	longest := 0
	for !engine.IsFinished() {
		for _, seat := range []string{testutil.SeatID(1), testutil.SeatID(2)} {
			hand, top, suit, myTurn := truth(engine, seat)
			got := parseCrazy(frameLines(renderSeat(t, engine, seat)))

			require.Equal(t, sortedCards(hand), sortedCards(got.hand), "hand of %d", len(hand))
			require.Equal(t, top, got.top)
			require.Equal(t, suit, got.suit)
			require.Equal(t, myTurn, got.myTurn)
			longest = max(longest, len(hand))
		}
		// Everyone draws, so the hands grow until the stock runs out.
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionDrawCard{}))
	}
	assert.Greater(t, longest, 20, "no hand grew long enough to wrap the strip")
}

// playable is the engine's own answer to whether seat could play any card now.
func playable(engine *game.Engine, seat string) bool {
	can := false
	engine.WithState(func(s *game.State) {
		for _, p := range s.Players {
			if p.ID != seat {
				continue
			}
			for _, c := range p.Cards {
				act := logic.ActionPlayCard{Card: c, ChosenSuit: deck.Spades}
				can = can || s.Rules.ValidateAction(s, act) == nil
			}
		}
	})
	return can
}

// Whole games where every move is what choose makes of the frame, typed into the real
// view: the engine must accept each one, and a bot must only draw when the engine
// agrees no card goes - drawing every turn would be legal and a useless load.
func TestChoose_EveryMoveIsLegalAndPlaysWhenItCan(t *testing.T) {
	t.Parallel()
	for g := range 10 {
		engine := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("alice", "bob", "carol"))
		require.NoError(t, engine.Start())
		t.Cleanup(engine.Close)

		for turn := 0; !engine.IsFinished(); turn++ {
			require.Less(t, turn, 500, "game %d never ended", g)
			seat := engine.CurrentPlayerID()
			n := slices.IndexFunc(engine.Snapshot().Players, func(p game.PlayerSnapshot) bool { return p.ID == seat })
			m := seatView(t, engine, n+1)
			frame := m.View().Content
			mv := parseCrazy(frameLines(frame)).choose()
			require.Equal(t, mv.keys == "d", !playable(engine, seat), "game %d turn %d: %q\n%s", g, turn, mv.keys, ansi.Strip(frame))

			m = press(m, mv.keys)
			if mv.wild {
				require.True(t, parseCrazy(frameLines(m.View().Content)).picker, "an eight opens the suit picker")
				press(m, keyEnter)
			}
			closeView(m)
			require.True(t, engine.IsFinished() || engine.CurrentPlayerID() != seat,
				"game %d turn %d: the engine refused %q", g, turn, mv.keys)
		}
	}
}

func TestSelectKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cursor int
		i      int
		want   string
	}{
		{name: "a digit reaches the first ten", cursor: 12, i: 7, want: "7"},
		{name: "arrows right past ten", cursor: 9, i: 11, want: keyRight + keyRight},
		{name: "arrows left past ten", cursor: 14, i: 12, want: keyLeft + keyLeft},
		{name: "no cursor counts from the first card", cursor: -1, i: 10, want: strings.Repeat(keyRight, 10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, crazyTable{cursor: tt.cursor}.selectKeys(tt.i))
		})
	}
}

// createKey, fed the real form after every key it asks for, arrives at a public
// casual Crazy Eights table of the wanted size and only then says enter.
func TestCreateKey_DrivesTheRealFormToTheTable(t *testing.T) {
	t.Parallel()
	for seats := 2; seats <= 6; seats++ {
		t.Run(strconv.Itoa(seats), func(t *testing.T) {
			t.Parallel()
			m := lobbyview.NewCreate(seatGlobal(t, lobby.NewManager(t.Context(), nil, catalog.NewRegistry()), 1))
			for range 20 {
				lines := frameLines(m.View().Content)
				key := createKey(lines, seats)
				if key == keyEnter {
					text := strings.Join(lines, "\n")
					assert.Contains(t, text, "> [ Create Lobby ]")
					assert.Contains(t, text, fmt.Sprintf("Max Players: < %d >", seats))
					assert.Contains(t, text, "< Public")
					assert.Contains(t, text, "< Casual")
					return
				}
				require.NotEmpty(t, key, "the form has no cursor on it")
				m = press(m, key)
			}
			t.Fatal("createKey never reached the submit row")
		})
	}
}

// The host publishes the code on its screen and waits for a full roster, so both
// have to be read right off the real lobby - including a roster too long to show.
func TestLobbyScreen_CodeAndGuests(t *testing.T) {
	t.Parallel()
	for guests := range 6 {
		t.Run(strconv.Itoa(guests), func(t *testing.T) {
			t.Parallel()
			mgr := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
			g := seatGlobal(t, mgr, 1)
			l, err := mgr.CreateLobby(g.Player, lobby.WithCardGame("crazy_eights"), lobby.WithMaxPlayers(6))
			require.NoError(t, err)
			for n := range guests {
				_, err := mgr.JoinLobbyByCode(t.Context(), l.Code(), seatGlobal(t, mgr, n+2).Player)
				require.NoError(t, err)
			}
			m := lobbyview.New(g, l)
			t.Cleanup(m.(router.Closer).Close)

			lines := frameLines(m.View().Content)
			assert.Equal(t, l.Code(), parseLobbyCode(lines))
			assert.Equal(t, guests, guestCount(lines))
		})
	}
}
