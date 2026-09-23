package uno

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

type Rules struct{}

var (
	_ game.Rules              = (*Rules)(nil)
	_ game.PlayerLeaveHandler = (*Rules)(nil)
	_ game.TurnTimeoutHandler = (*Rules)(nil)
	_ game.StandingScorer     = (*Rules)(nil)
)

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 10 }

func (r *Rules) InitialDeck() []deck.Card { return initialDeck() }
func (r *Rules) InitialDealCount() int    { return 7 }

func (r *Rules) TimeoutAction(_ *game.State) game.Action {
	return ActionDrawCard{}
}

func (r *Rules) OnGameStart(state *game.State) error {
	extra := &State{Direction: 1}
	state.Extra = extra

	// Official Uno never starts on a Wild; redraw until a colored card surfaces.
	top, err := game.OpenDiscard(state, func(c deck.Card) bool { return !isWild(c.Rank) })
	if err != nil {
		return fmt.Errorf("open the uno discard pile: %w", err)
	}
	extra.CurrentColor = top.Suit
	r.applyOpeningCard(state, extra, top)
	return nil
}

// applyOpeningCard plays the action printed on the card that starts the discard
// pile. Nobody played it, so the effect lands on the seat the engine put on turn:
// they lose the turn, draw, or find the table already running the other way. The
// opening card is never a Wild, so there is no colour to choose.
func (r *Rules) applyOpeningCard(state *game.State, extra *State, card deck.Card) {
	first := state.CurrentTurn
	switch card.Rank {
	case Skip:
		first = r.advance(state, extra, 1)
	case DrawTwo:
		if !drawCardsInto(state, state.CurrentTurn, 2) {
			extra.Passes++
		}
		first = r.advance(state, extra, 1)
	case Reverse:
		if len(state.Players) == 2 {
			// Heads-up a Reverse is a Skip, exactly as it is mid-hand.
			first = r.advance(state, extra, 1)
			break
		}
		// Deviation: the official rules give the turn to the dealer's right, which
		// this table has no dealer to measure from. The seat the engine picked keeps
		// it and only the direction flips.
		extra.Direction = -1
	default:
		// A number card (a Wild cannot open, see OnGameStart): the first seat plays.
	}
	state.CurrentTurn = first
	state.OverrideNextTurn = &first
}

type ActionPlayCard struct {
	Card        deck.Card
	ChosenColor deck.Suit // required for Wild/WildDrawFour, ignored otherwise
}

func (a ActionPlayCard) Name() string { return "uno.PlayCard" }

type ActionDrawCard struct{}

func (a ActionDrawCard) Name() string { return "uno.DrawCard" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	switch a := action.(type) {
	case ActionPlayCard:
		// Peeked here, not above the switch: a draw is legal whatever is on the pile,
		// and TimeoutAction plays a draw, so making it depend on the top card is what
		// would freeze a seat on a board that somehow has no discard.
		topCard, ok := state.Discard.Peek()
		if !ok {
			return errors.New("no cards in discard")
		}
		hand := state.Players[state.CurrentTurn].Cards
		if !slices.Contains(hand, a.Card) {
			return errors.New("you don't have that card")
		}
		if isWild(a.Card.Rank) {
			if !deck.IsSuit(a.ChosenColor) {
				return errors.New("must choose a valid color")
			}
			// A Wild Draw Four is the one card the official rules gate on the hand
			// behind it: it may only be played by someone with nothing of the
			// current colour to play instead.
			//
			// Deviation: the paper game lets the next player challenge a suspect
			// WD4 and inspect the hand. Here the server holds every hand already,
			// so the gate is enforced up front instead - the illegal play is
			// refused rather than punished, and there is nothing to challenge.
			if a.Card.Rank == WildDrawFour && hasColor(hand, extra.CurrentColor) {
				return errors.New("wild draw four needs a hand with no card of the current color")
			}
			return nil
		}
		if a.Card.Suit == extra.CurrentColor {
			return nil
		}
		if a.Card.Rank == topCard.Rank && !isWild(topCard.Rank) {
			return nil
		}
		return errors.New("card doesn't match color, number, or symbol")

	case ActionDrawCard:
		return nil
	}
	return errors.New("unknown action")
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	switch a := action.(type) {
	case ActionPlayCard:
		actor := state.Players[state.CurrentTurn]
		actor.Cards = deck.RemoveOne(actor.Cards, a.Card)
		state.Discard.AddCard(a.Card)
		extra.Passes = 0

		if isWild(a.Card.Rank) {
			extra.CurrentColor = a.ChosenColor
		} else {
			extra.CurrentColor = a.Card.Suit
		}

		switch a.Card.Rank {
		case Skip:
			next := r.advance(state, extra, 2)
			state.OverrideNextTurn = &next
		case Reverse:
			r.applyReverse(state, extra)
		case DrawTwo:
			r.applyForcedDraw(state, extra, 2)
		case WildDrawFour:
			r.applyForcedDraw(state, extra, 4)
		default:
			next := r.advance(state, extra, 1)
			state.OverrideNextTurn = &next
		}
	case ActionDrawCard:
		r.applyVoluntaryDraw(state, extra)
	}
	return nil
}

// Every action sets OverrideNextTurn: the engine's own advance only steps +1, so a
// reversed table would otherwise ignore Direction.
func (r *Rules) advance(state *game.State, extra *State, steps int) int {
	n := len(state.Players)
	if n == 0 {
		return 0
	}
	delta := int(extra.Direction) * steps
	return ((state.CurrentTurn+delta)%n + n) % n
}

func (r *Rules) applyReverse(state *game.State, extra *State) {
	if len(state.Players) == 2 {
		// Reverse in 2-player acts as Skip (same seat again).
		next := state.CurrentTurn
		state.OverrideNextTurn = &next
		return
	}
	extra.Direction *= -1
	next := r.advance(state, extra, 1)
	state.OverrideNextTurn = &next
}

func (r *Rules) applyForcedDraw(state *game.State, extra *State, n int) {
	victim := r.advance(state, extra, 1)
	if !drawCardsInto(state, victim, n) {
		extra.Passes++
	}
	next := r.advance(state, extra, 2)
	state.OverrideNextTurn = &next
}

// Deviation: the drawn card cannot be played immediately. The official rules let a
// player play the card they just drew; here the draw ends the turn, which keeps a
// draw a single action with no follow-up state for a disconnect to strand.
func (r *Rules) applyVoluntaryDraw(state *game.State, extra *State) {
	actor := state.CurrentTurn
	if !drawCardsInto(state, actor, 1) {
		extra.Passes++
	} else {
		extra.Passes = 0
	}
	next := r.advance(state, extra, 1)
	state.OverrideNextTurn = &next
}

// drawCardsInto draws up to n cards into the seat, reshuffling discard->stock
// when needed. Returns false only when zero cards were drawn (deadlock).
func drawCardsInto(state *game.State, playerIdx, n int) bool {
	if playerIdx < 0 || playerIdx >= len(state.Players) || n <= 0 {
		return false
	}
	p := state.Players[playerIdx]
	drew := 0
	for range n {
		card, ok := game.DrawWithReshuffle(state)
		if !ok {
			break
		}
		p.Cards = append(p.Cards, card)
		drew++
	}
	return drew > 0
}

func (r *Rules) AfterAction(_ *game.State, _ game.Action) error {
	return nil
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	extra, ok := state.Extra.(*State)
	if !ok {
		return false
	}
	return game.HandEmptyOrAllPassed(state, extra.Passes)
}

func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	extra.leaverWasOnTurn = state.CurrentTurn == slices.IndexFunc(state.Players,
		func(p *game.Player) bool { return p != nil && p.ID == playerID })
	game.LeaveShedGame(state, &extra.ShedState, playerID)
}

// AfterPlayerRemoved settles the cursor when the seat on turn left a counterclockwise
// table. The engine's generic fix-up leaves the cursor where the leaver sat, which is
// now the seat one step clockwise - the right answer at Direction +1 and the wrong
// neighbour at -1, where it skips a player.
func (r *Rules) AfterPlayerRemoved(state *game.State, removedIndex int) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	onTurn := extra.leaverWasOnTurn
	extra.leaverWasOnTurn = false

	n := len(state.Players)
	// Below two seats there is no neighbour to hand the turn to, and a non-nil
	// OverrideNextTurn would tell the engine the last seat still has work - which
	// costs the survivor the forfeit win.
	if !onTurn || extra.Direction >= 0 || n < 2 {
		return
	}
	// Counterclockwise the turn owes to the seat before the leaver, which keeps its
	// index through the delete when there is one and wraps to the last seat when
	// the leaver sat first.
	next := ((removedIndex-1)%n + n) % n
	state.CurrentTurn = next
	state.OverrideNextTurn = &next
}

func (r *Rules) Standings(state *game.State) []*game.Player { return game.ShedStandings(state) }

func (r *Rules) StandingScore(_ *game.State, p *game.Player) int { return game.ShedScore(p) }

// Compile-time proof of the optional hook: without it, deleting StandingScore still
// compiles and the engine silently splits every draw by seat order.
var _ game.StandingScorer = (*Rules)(nil)
