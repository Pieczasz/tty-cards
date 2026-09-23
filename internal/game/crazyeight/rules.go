package crazyeight

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

// TimeoutAction draws. ValidateAction always accepts a draw, and on an exhausted
// board it degrades into the forced pass the turn loop already handles - so it is
// the one move that cannot fail. Picking a card to play would also spend a card the
// player may have been holding for a reason.
func (r *Rules) TimeoutAction(_ *game.State) game.Action {
	return ActionDrawCard{}
}

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 6 }

func (r *Rules) InitialDeck() []deck.Card {
	return deck.StandardDeck()
}

func (r *Rules) InitialDealCount() int {
	return 7
}

func (r *Rules) OnGameStart(state *game.State) error {
	extra := &State{CurrentSuit: deck.NoSuit}
	state.Extra = extra

	// An Eight is the wild card and the deck cannot name a suit for the one it turns
	// up itself, which would leave the opening suit set by the card's own printed
	// suit while every player sees a wild. Redraw until a plain card opens the pile,
	// the same way uno refuses to open on a Wild.
	top, err := game.OpenDiscard(state, func(c deck.Card) bool { return c.Rank != deck.Eight })
	if err != nil {
		return fmt.Errorf("open the crazy eights discard pile: %w", err)
	}
	extra.CurrentSuit = top.Suit
	return nil
}

// ActionPlayCard carries exactly one card by construction: a single field cannot
// express the zero- or multi-card requests a slice could, so ApplyAction has no
// invalid length to guard against.
type ActionPlayCard struct {
	Card deck.Card
	Suit deck.Suit
}

func (a ActionPlayCard) Name() string { return "crazyeight.PlayCard" }

type ActionDrawCard struct{}

func (a ActionDrawCard) Name() string { return "crazyeight.DrawCard" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	switch action := action.(type) {
	case ActionPlayCard:
		// Peeked here, not above the switch: a draw is legal whatever is on the pile,
		// and TimeoutAction plays a draw, so making it depend on the top card is what
		// would freeze a seat on a board that somehow has no discard.
		topCard, ok := state.Discard.Peek()
		if !ok {
			return errors.New("no cards in discard pile")
		}
		card := action.Card

		if !slices.Contains(state.Players[state.CurrentTurn].Cards, card) {
			return errors.New("you don't have that card")
		}

		if card.Rank == deck.Eight {
			if !deck.IsSuit(action.Suit) {
				return errors.New("must choose a suit when playing an eight")
			}
			return nil
		}
		if card.Suit == extra.CurrentSuit {
			return nil
		}
		if card.Rank == topCard.Rank {
			return nil
		}
		return errors.New("card doesn't match top discard")

	case ActionDrawCard:
		// Always legal: with cards available it draws, otherwise it is a forced
		// pass so an exhausted board can never soft-lock the turn loop.
		return nil
	}

	return errors.New("unknown action")
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	p := state.Players[state.CurrentTurn]

	switch action := action.(type) {
	case ActionPlayCard:
		card := action.Card

		p.Cards = deck.RemoveOne(p.Cards, card)
		state.Discard.AddCard(card)

		// An Eight names its own suit; ValidateAction has already refused one that
		// does not, so there is no "no suit chosen" case left to fall through.
		if card.Rank == deck.Eight {
			extra.CurrentSuit = action.Suit
		} else {
			extra.CurrentSuit = card.Suit
		}
		extra.Passes = 0

	case ActionDrawCard:
		drawn, ok := game.DrawWithReshuffle(state)
		if !ok {
			extra.Passes++ // stock and discard exhausted: this turn is a forced pass
			return nil
		}
		p.Cards = append(p.Cards, drawn)
		extra.Passes = 0
	}
	return nil
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

// OnPlayerLeave returns the departing player's cards to the stock so the deck
// stays whole; the engine removes the player afterward.
func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	game.LeaveShedGame(state, &extra.ShedState, playerID)
}

// AfterPlayerRemoved is a no-op; the engine's generic cursor handling suffices.
func (r *Rules) AfterPlayerRemoved(_ *game.State, _ int) {}

// Standings ranks by fewest cards held. Deviation: the paper game scores a hand by
// the pip value of the cards left in each hand, which needs a running match total
// this table does not keep - one hand, and the shortest hand takes it.
func (r *Rules) Standings(state *game.State) []*game.Player { return game.ShedStandings(state) }

func (r *Rules) StandingScore(_ *game.State, p *game.Player) int { return game.ShedScore(p) }

// Compile-time proof of the optional hook: without it, deleting StandingScore still
// compiles and the engine silently splits every draw by seat order.
var _ game.StandingScorer = (*Rules)(nil)
