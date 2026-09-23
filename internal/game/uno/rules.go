// Package uno is Uno for two to ten players: colour, number and symbol matching,
// Skip, Reverse, the draw cards and Wilds, over the shared shedding-game helpers.
package uno

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/shed"
)

// Rules implements Uno; see the Deviation notes for where it departs from the box.
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

// TimeoutAction draws: ValidateAction always accepts a draw, so it cannot be refused.
func (r *Rules) TimeoutAction(_ *game.State) game.Action {
	return ActionDrawCard{}
}

func (r *Rules) OnGameStart(state *game.State) error {
	extra := &State{Direction: 1}
	state.Extra = extra

	// Official Uno never starts on a Wild; redraw until a colored card surfaces.
	top, err := shed.OpenDiscard(state, func(c deck.Card) bool { return !isWild(c.Rank) })
	if err != nil {
		return fmt.Errorf("open the uno discard pile: %w", err)
	}
	extra.CurrentColor = top.Suit
	applyOpeningCard(state, extra, top)
	return nil
}

// applyOpeningCard plays the action printed on the card that starts the discard
// pile. Nobody played it, so the effect lands on the seat the engine put on turn:
// they lose the turn, draw, or find the table already running the other way. The
// opening card is never a Wild, so there is no colour to choose.
func applyOpeningCard(state *game.State, extra *State, card deck.Card) {
	first := state.CurrentTurn
	switch card.Rank {
	case Skip:
		first = advance(state, extra, 1)
	case DrawTwo:
		extra.RecordDraw(shed.DrawInto(state, state.Players[state.CurrentTurn], 2))
		first = advance(state, extra, 1)
	case Reverse:
		if len(state.Players) == 2 {
			// Heads-up a Reverse is a Skip, exactly as it is mid-hand.
			first = advance(state, extra, 1)
			break
		}
		// Deviation: the official rules give the turn to the dealer's right, which
		// this table has no dealer to measure from. The seat the engine picked keeps
		// it and only the direction flips.
		extra.Direction = -1
	default:
		// A number card (a Wild cannot open, see OnGameStart): the first seat plays.
	}
	state.SetTurn(first)
}

// ActionPlayCard plays Card onto the discard pile, naming the colour play continues in
// when it is a Wild.
type ActionPlayCard struct {
	Card        deck.Card
	ChosenColor deck.Suit // required for Wild/WildDrawFour, ignored otherwise
}

func (a ActionPlayCard) Name() string { return "uno.PlayCard" }

// ActionDrawCard draws one card and ends the turn; on a spent board it is a pass.
type ActionDrawCard struct{}

func (a ActionDrawCard) Name() string { return "uno.DrawCard" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	switch a := action.(type) {
	case ActionPlayCard:
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return shed.ValidatePlay(state, a.Card, func(topCard deck.Card) error {
			return validatePlay(state.Players[state.CurrentTurn].Cards, extra, a, topCard)
		})

	case ActionDrawCard:
		return nil
	}
	return errUnknownAction
}

var errUnknownAction = errors.New("unknown action")

func validatePlay(hand []deck.Card, extra *State, a ActionPlayCard, topCard deck.Card) error {
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
		state.Discard.Add(a.Card)
		extra.Passes = 0

		if isWild(a.Card.Rank) {
			extra.CurrentColor = a.ChosenColor
		} else {
			extra.CurrentColor = a.Card.Suit
		}

		switch a.Card.Rank {
		case Skip:
			state.OverrideTurn(advance(state, extra, 2))
		case Reverse:
			applyReverse(state, extra)
		case DrawTwo:
			applyForcedDraw(state, extra, 2)
		case WildDrawFour:
			applyForcedDraw(state, extra, 4)
		default:
			state.OverrideTurn(advance(state, extra, 1))
		}
	case ActionDrawCard:
		applyVoluntaryDraw(state, extra)
	}
	return nil
}

// advance is the seat steps turns on in the table's direction. Every action sets
// OverrideNextTurn from it: the engine's own advance only steps +1, so a reversed table
// would otherwise ignore Direction.
func advance(state *game.State, extra *State, steps int) int {
	return game.SeatAt(state.CurrentTurn+int(extra.Direction)*steps, len(state.Players))
}

func applyReverse(state *game.State, extra *State) {
	if len(state.Players) == 2 {
		// Reverse in 2-player acts as Skip (same seat again).
		state.OverrideTurn(state.CurrentTurn)
		return
	}
	extra.Direction *= -1
	state.OverrideTurn(advance(state, extra, 1))
}

// applyForcedDraw deals n cards to the next seat and skips it. A draw the board cannot
// cover charges the deadlock count like any other.
func applyForcedDraw(state *game.State, extra *State, n int) {
	victim := state.Players[advance(state, extra, 1)]
	extra.RecordDraw(shed.DrawInto(state, victim, n))
	state.OverrideTurn(advance(state, extra, 2))
}

// Deviation: the drawn card cannot be played immediately. The official rules let a
// player play the card they just drew; here the draw ends the turn, which keeps a
// draw a single action with no follow-up state for a disconnect to strand.
func applyVoluntaryDraw(state *game.State, extra *State) {
	extra.RecordDraw(shed.DrawInto(state, state.Players[state.CurrentTurn], 1))
	state.OverrideTurn(advance(state, extra, 1))
}

func (r *Rules) AfterAction(_ *game.State, _ game.Action) error {
	return nil
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	extra, ok := state.Extra.(*State)
	if !ok {
		return false
	}
	return shed.HandEmptyOrAllPassed(state, extra.Passes)
}

func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	extra.leaverWasOnTurn = state.CurrentTurn == slices.IndexFunc(state.Players,
		func(p *game.Player) bool { return p != nil && p.ID == playerID })
	shed.Leave(state, &extra.State, playerID)
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
	state.SetTurn(game.SeatAt(removedIndex-1, n))
}

func (r *Rules) Standings(state *game.State) []*game.Player { return shed.Standings(state) }

func (r *Rules) StandingScore(_ *game.State, p *game.Player) int { return shed.Score(p) }
