// Package crazyeight is Crazy Eights for two to six players: match the suit or the
// rank of the card in play, and an Eight names the suit, over the shared
// shedding-game helpers.
package crazyeight

import (
	"errors"
	"fmt"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/shed"
)

// The seat range a Crazy Eights table deals for.
const (
	MinPlayers = 2
	MaxPlayers = 6
)

// dealCount is the hand every seat is dealt. Deviation: the common rules deal seven
// only heads-up and five to three or more players. Seven at every size leaves a
// six-seat table a stock of nine, so the discard is reshuffled into it sooner.
const dealCount = 7

// Rules implements Crazy Eights; one hand is the whole game.
type Rules struct{}

var (
	_ game.Rules              = (*Rules)(nil)
	_ game.PlayerLeaveHandler = (*Rules)(nil)
)

// TimeoutAction draws. ValidateAction always accepts a draw, and on an exhausted
// board it degrades into the forced pass the turn loop already handles - so it is
// the one move that cannot fail. Picking a card to play would also spend a card the
// player may have been holding for a reason.
func (r *Rules) TimeoutAction(_ *game.State) game.Action {
	return ActionDrawCard{}
}

func (r *Rules) OnGameStart(state *game.State) error {
	if err := shed.Deal(state, deck.Standard(), dealCount); err != nil {
		return fmt.Errorf("deal crazy eights: %w", err)
	}
	return openTable(state)
}

// openTable is OnGameStart once the hands are dealt: the opening discard and the suit
// it names.
func openTable(state *game.State) error {
	extra := &State{CurrentSuit: deck.NoSuit}
	state.Extra = extra

	// An Eight is the wild card and the deck cannot name a suit for the one it turns
	// up itself, which would leave the opening suit set by the card's own printed
	// suit while every player sees a wild. Redraw until a plain card opens the pile,
	// the same way uno refuses to open on a Wild.
	top, err := shed.OpenDiscard(state, func(c deck.Card) bool { return c.Rank != deck.Eight })
	if err != nil {
		return fmt.Errorf("open the crazy eights discard pile: %w", err)
	}
	extra.CurrentSuit = top.Suit
	return nil
}

// ActionPlayCard plays Card onto the discard pile; ChosenSuit is the one an Eight names.
// It carries exactly one card by construction: a single field cannot express the zero-
// or multi-card requests a slice could, so ApplyAction has no invalid length to guard
// against.
type ActionPlayCard struct {
	Card       deck.Card
	ChosenSuit deck.Suit
}

func (a ActionPlayCard) Name() string { return "crazyeight.PlayCard" }

// ActionDrawCard draws one card and ends the turn; on a spent board it is a pass.
type ActionDrawCard struct{}

func (a ActionDrawCard) Name() string { return "crazyeight.DrawCard" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra := extraOf(state)

	switch action := action.(type) {
	case ActionPlayCard:
		card := action.Card
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return shed.ValidatePlay(state, card, func(topCard deck.Card) error {
			if card.Rank == deck.Eight {
				if !deck.IsSuit(action.ChosenSuit) {
					return errors.New("must choose a suit when playing an eight")
				}
				return nil
			}
			if card.Suit == extra.CurrentSuit || card.Rank == topCard.Rank {
				return nil
			}
			return errors.New("card doesn't match top discard")
		})

	case ActionDrawCard:
		// Always legal: with cards available it draws, otherwise it is a forced
		// pass so an exhausted board can never soft-lock the turn loop.
		return nil
	}

	return game.ErrUnknownAction
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra := extraOf(state)
	p := state.Players[state.CurrentTurn]

	switch action := action.(type) {
	case ActionPlayCard:
		card := action.Card

		p.Cards = deck.RemoveOne(p.Cards, card)
		state.Discard.Add(card)

		// An Eight names its own suit; ValidateAction has already refused one that
		// does not, so there is no "no suit chosen" case left to fall through.
		if card.Rank == deck.Eight {
			extra.CurrentSuit = action.ChosenSuit
		} else {
			extra.CurrentSuit = card.Suit
		}
		extra.Passes = 0

	case ActionDrawCard:
		// Nothing drawn means stock and discard are both spent: a forced pass.
		extra.RecordDraw(shed.DrawInto(state, p, 1))
	default:
		return game.ErrUnknownAction
	}
	return nil
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	return shed.HandEmptyOrAllPassed(state, extraOf(state).Passes)
}

// OnPlayerLeave returns the departing player's cards to the stock so the deck
// stays whole; the engine removes the player afterward.
func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra := extraOf(state)
	shed.Leave(state, &extra.State, playerID)
}

// AfterPlayerRemoved leaves it to the engine: its generic cursor handling suffices.
func (r *Rules) AfterPlayerRemoved(_ *game.State, _ int) game.LeaveOutcome { return game.LeaveSettled }

// Standings ranks by fewest cards held. Deviation: the paper game scores a hand by
// the pip value of the cards left in each hand, which needs a running match total
// this table does not keep - one hand, and the shortest hand takes it.
func (r *Rules) Standings(state *game.State) []game.Standing { return shed.Standings(state) }
