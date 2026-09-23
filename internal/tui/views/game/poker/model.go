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

// Seat is one player position around the table for rendering.
type Seat struct {
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

type Model struct {
	gameview.Session

	seats         []Seat
	board         []deck.Card
	pot           uint
	sidePots      int
	street        string
	currentBet    uint
	toCall        uint
	myChips       uint
	handComplete  bool
	matchComplete bool
	handNumber    int
	handsTotal    int
	winnerName    string

	// raiseMin..raiseMax is what logic.RaiseBounds allows the hero; raiseOK is false
	// when there is no raise to make at all.
	raiseMin, raiseMax uint
	raiseOK            bool

	raising     bool
	raiseAmount uint
}

// New creates a Hold'em TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	// A subscribe failure is kept on the Session's ActionErr, which the hero band shows.
	session, _ := gameview.NewSession(global, engine, "poker")
	m := &Model{Session: session}
	m.syncState()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Listen(), m.ClockTick())
}

func (m *Model) syncState() {
	m.seats = nil
	m.board = nil
	m.pot = 0
	m.sidePots = 0
	m.street = ""
	m.currentBet = 0
	m.toCall = 0
	m.raiseMin, m.raiseMax, m.raiseOK = 0, 0, false
	m.myChips = 0
	m.handComplete = false
	m.matchComplete = false
	m.handNumber = 0
	m.handsTotal = 0
	m.winnerName = ""

	heroID := ""
	if m.Bound != nil {
		heroID = m.Bound.PlayerID()
	}

	// Poker needs State.Players for hole cards at showdown, so the Frame callback
	// takes the live *State (not only Extra) and fills betting scalars in the same
	// hold as Base.MyTurn - a split Sync+WithState let an opponent act between them.
	m.Sync(func(state *game.State) {
		m.matchComplete = state.Phase == game.Finished
		m.handComplete = state.Phase == game.Finished
		if state.Winner != nil {
			m.winnerName = state.Winner.DisplayName()
		}

		e, ok := state.Extra.(*logic.State)
		if !ok || e == nil {
			return
		}
		m.pot = e.MainPool
		m.sidePots = len(e.Pots)
		m.street = e.Phase.String()
		m.currentBet = e.CurrentBet
		m.raiseMin, m.raiseMax, m.raiseOK = logic.RaiseBounds(state, heroID)
		m.toCall = logic.ToCall(e, heroID)
		m.myChips = e.PlayerChips[heroID]
		m.handComplete = e.HandComplete || state.Phase == game.Finished
		m.matchComplete = e.MatchComplete || state.Phase == game.Finished
		m.handNumber = e.HandNumber
		m.handsTotal = e.HandsTotal
		// Winners holds whoever took the last pot; the match itself is won by the
		// biggest stack, which is the winner the engine settles on.
		if len(e.Winners) > 0 && !m.matchComplete {
			m.winnerName = e.Winners[0].DisplayName()
		}

		m.board = slices.Clone(e.Table)
		m.seats = buildSeats(state, e, heroID)
	})

	// A half-built raise belongs to the hero's turn: once the action has moved on,
	// whether by folding, a timeout or the hand ending, the prompt goes with it.
	if !m.Base.MyTurn {
		m.raising = false
	}
	if m.raising {
		m.raiseAmount = m.clampRaise(m.raiseAmount)
	}
}

// buildSeats snapshots every seat for rendering. Hole cards are copied out only
// for the hero, or for anyone still live once the hand is shown down - everyone
// else gets a hand size and nothing more. A pot that nobody contested is won
// face-down: with hands left to play, showing those cards would hand the table a
// free read. The match ending is no exception: a last pot won face-down keeps its
// cards hidden too. Caller must hold the state lock.
func buildSeats(state *game.State, extra *logic.State, heroID string) []Seat {
	reveal := extra.ReachedShowdown

	seats := make([]Seat, 0, len(state.Players))
	for i, p := range state.Players {
		if p == nil {
			continue
		}
		s := Seat{
			PlayerID: p.ID,
			Name:     p.DisplayName(),
			Chips:    extra.PlayerChips[p.ID],
			Bet:      extra.PlayerBets[p.ID],
			Folded:   extra.Folded[p.ID],
			AllIn:    extra.PlayersAllIn[p.ID],
			IsDealer: i == extra.DealerIndex,
			IsSB:     i == extra.SBIndex,
			IsBB:     i == extra.BBIndex,
			IsTurn:   state.Phase == game.Playing && state.CurrentTurn == i && !extra.HandComplete,
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
func (m *Model) clampRaise(amount uint) uint {
	return min(max(amount, m.raiseMin), m.raiseMax)
}

func (m *Model) heroSeat() *Seat {
	for i := range m.seats {
		if m.seats[i].IsHero {
			return &m.seats[i]
		}
	}
	return nil
}

func (m *Model) canCheck() bool {
	return m.Base.MyTurn && m.toCall == 0 && !m.handComplete
}

func (m *Model) canCall() bool {
	return m.Base.MyTurn && m.toCall > 0 && !m.handComplete
}

func (m *Model) canRaise() bool {
	return m.Base.MyTurn && !m.handComplete && m.raiseOK
}

func (m *Model) canAllIn() bool {
	hero := m.heroSeat()
	return m.Base.MyTurn && !m.handComplete && hero != nil && hero.Chips > 0
}

func (m *Model) canFold() bool {
	return m.Base.MyTurn && !m.handComplete
}

// canDeal reports whether the hero is the one holding the button between hands,
// and so the one who deals the next one.
func (m *Model) canDeal() bool {
	return m.handComplete && !m.matchComplete && m.Base.MyTurn
}

// heroBusted reports whether the hero has lost their stack. They keep their seat
// so the remaining players' pots and standings stay intact, but they cannot act
// or deal for the rest of the match.
func (m *Model) heroBusted() bool {
	hero := m.heroSeat()
	return hero != nil && hero.Chips == 0
}
