// Package poker is the Texas Hold'em table view: every seat around the board, the
// hero's own seat and action bar, the raise prompt and the between-hands results.
package poker

import (
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/poker"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
)

// seat is one player position around the table for rendering.
type seat struct {
	PlayerID string
	Name     string
	Chips    uint
	Bet      uint
	Folded   bool
	AllIn    bool
	IsDealer bool
	IsSB     bool
	IsBB     bool
	IsTurn   bool
	IsHero   bool
	HandSize int
	Hole     []deck.Card
}

// table is everything syncState lifts out of the engine for one frame. It is rebuilt
// whole on every sync, so nothing the engine stopped reporting can outlive the frame
// that last saw it.
type table struct {
	seats         []seat
	board         []deck.Card
	pot           uint
	sidePots      int
	street        string
	currentBet    uint
	toCall        uint
	handComplete  bool
	matchComplete bool
	handNumber    int
	handsTotal    int
	winnerName    string

	// raiseMin..raiseMax is what logic.RaiseBounds allows the hero; raiseOK is false
	// when there is no raise to make at all.
	raiseMin, raiseMax uint
	raiseOK            bool
}

type model struct {
	gameview.Session
	table

	raising     bool
	raiseAmount uint
}

// New creates a Hold'em TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	// A subscribe failure is kept on the Session's ActionErr, which the hero band shows.
	session, _ := gameview.NewSession(global, engine, "poker")
	m := &model{Session: session}
	m.syncState()
	return m
}

func (m *model) syncState() {
	heroID := ""
	if m.Bound != nil {
		heroID = m.Bound.PlayerID()
	}

	// Poker needs State.Players for hole cards at showdown, so the Frame callback
	// takes the live *State (not only Extra) and fills betting scalars in the same
	// hold as Base.MyTurn - a split Sync+WithState let an opponent act between them.
	var t table
	m.Sync(func(state *game.State) { t = readTable(state, heroID) })
	m.table = t

	// A half-built raise belongs to the hero's turn: once the action has moved on,
	// whether by folding, a timeout or the hand ending, the prompt goes with it.
	if !m.Base.MyTurn {
		m.raising = false
	}
	if m.raising {
		m.raiseAmount = m.clampRaise(m.raiseAmount)
	}
}

// readTable copies one frame of the table out of the live state. Caller must hold the
// state lock.
func readTable(state *game.State, heroID string) table {
	finished := state.Phase == game.Finished
	t := table{matchComplete: finished, handComplete: finished}
	if state.Winner != nil {
		t.winnerName = state.Winner.DisplayName()
	}

	e, ok := state.Extra.(*logic.State)
	if !ok || e == nil {
		return t
	}
	t.pot = e.Pool
	t.sidePots = len(e.Pots)
	t.street = e.Phase.String()
	t.currentBet = e.CurrentBet
	t.raiseMin, t.raiseMax, t.raiseOK = logic.RaiseBounds(state, heroID)
	t.toCall = e.ToCall(heroID)
	t.handComplete = e.HandComplete() || finished
	t.matchComplete = e.MatchComplete || finished
	t.handNumber = e.HandNumber
	t.handsTotal = e.HandsTotal
	// Winners holds whoever took the last pot; the match itself is won by the
	// biggest stack, which is the winner the engine settles on.
	if len(e.Winners) > 0 && !t.matchComplete {
		t.winnerName = e.Winners[0].DisplayName()
	}

	t.board = slices.Clone(e.Table)
	t.seats = buildSeats(state, e, heroID)
	return t
}

// buildSeats snapshots every seat for rendering. Hole cards are copied out only
// for the hero, or for anyone still live once the hand is shown down - everyone
// else gets a hand size and nothing more. A pot that nobody contested is won
// face-down: with hands left to play, showing those cards would hand the table a
// free read. The match ending is no exception: a last pot won face-down keeps its
// cards hidden too. Caller must hold the state lock.
func buildSeats(state *game.State, extra *logic.State, heroID string) []seat {
	reveal := extra.ReachedShowdown

	seats := make([]seat, 0, len(state.Players))
	for i, p := range state.Players {
		if p == nil {
			continue
		}
		var money logic.Seat
		if ls := extra.Seats[p.ID]; ls != nil {
			money = *ls
		}
		s := seat{
			PlayerID: p.ID,
			Name:     p.DisplayName(),
			Chips:    money.Chips,
			Bet:      money.Bet,
			Folded:   money.Folded,
			AllIn:    money.AllIn,
			IsDealer: i == extra.DealerIndex,
			IsSB:     i == extra.SBIndex,
			IsBB:     i == extra.BBIndex,
			IsTurn:   state.Phase == game.Playing && state.CurrentTurn == i && !extra.HandComplete(),
			IsHero:   p.ID == heroID,
			HandSize: len(p.Cards),
		}
		if s.IsHero || (reveal && !s.Folded) {
			s.Hole = slices.Clone(p.Cards)
		}
		seats = append(seats, s)
	}
	return seats
}

// clampRaise holds a raise-to amount within the band the rules accept.
func (m *model) clampRaise(amount uint) uint {
	return min(max(amount, m.raiseMin), m.raiseMax)
}

func (m *model) heroSeat() *seat {
	for i := range m.seats {
		if m.seats[i].IsHero {
			return &m.seats[i]
		}
	}
	return nil
}

func (m *model) canCheck() bool {
	return m.Base.MyTurn && m.toCall == 0 && !m.handComplete
}

func (m *model) canCall() bool {
	return m.Base.MyTurn && m.toCall > 0 && !m.handComplete
}

func (m *model) canRaise() bool {
	return m.Base.MyTurn && !m.handComplete && m.raiseOK
}

func (m *model) canAllIn() bool {
	hero := m.heroSeat()
	return m.Base.MyTurn && !m.handComplete && hero != nil && hero.Chips > 0
}

func (m *model) canFold() bool {
	return m.Base.MyTurn && !m.handComplete
}

// canDeal reports whether the hero is the one holding the button between hands,
// and so the one who deals the next one.
func (m *model) canDeal() bool {
	return m.handComplete && !m.matchComplete && m.Base.MyTurn
}

// heroBusted reports whether the hero has lost their stack. They keep their seat
// so the remaining players' pots and standings stay intact, but they cannot act
// or deal for the rest of the match.
func (m *model) heroBusted() bool {
	hero := m.heroSeat()
	return hero != nil && hero.Chips == 0
}
