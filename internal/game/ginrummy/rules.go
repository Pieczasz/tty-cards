// Package ginrummy is two-handed Gin Rummy played to a target score: draw, discard,
// knock on ten or less, gin and undercut bonuses, and layoffs on the knocker's melds.
package ginrummy

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// Rules implements Gin Rummy over a match of hands.
type Rules struct{}

var (
	_ game.Rules               = (*Rules)(nil)
	_ game.TurnTimeoutHandler  = (*Rules)(nil)
	_ game.TurnDurationHandler = (*Rules)(nil)
	_ game.StandingScorer      = (*Rules)(nil)
	// No PlayerLeaveHandler: gin is strictly 2-player (MinPlayers == MaxPlayers == 2).
	// When a player leaves mid-hand, the match ends immediately via the engine's
	// last-player-standing path. There is no shared state to clean up.
)

// ActionDrawStock takes the top card of the stock.
type ActionDrawStock struct{}

func (a ActionDrawStock) Name() string { return "ginrummy.DrawStock" }

// ActionDrawDiscard takes the upcard, which may then not be discarded straight back.
type ActionDrawDiscard struct{}

func (a ActionDrawDiscard) Name() string { return "ginrummy.DrawDiscard" }

// ActionDiscard lays Card on the discard pile, ending the turn.
type ActionDiscard struct {
	Card deck.Card
}

func (a ActionDiscard) Name() string { return "ginrummy.Discard" }

// ActionKnock discards and ends the hand, which the rest of the hand is scored on.
type ActionKnock struct {
	Discard deck.Card
}

func (a ActionKnock) Name() string { return "ginrummy.Knock" }

// ActionNextHand deals the next hand; only the seat that acts first in it may.
type ActionNextHand struct{}

func (a ActionNextHand) Name() string { return "ginrummy.NextHand" }

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 2 }

func (r *Rules) InitialDeck() []deck.Card { return deck.Standard() }

// InitialDealCount is zero: beginHand owns the deal for every hand of the match.
func (r *Rules) InitialDealCount() int { return 0 }

func (r *Rules) OnGameStart(state *game.State) error {
	if len(state.Players) != 2 {
		return fmt.Errorf("gin rummy requires exactly 2 players, got %d", len(state.Players))
	}
	extra := &State{
		FirstActor:       state.CurrentTurn,
		CumulativeScores: make(map[string]int, len(state.Players)),
	}
	for _, p := range state.Players {
		extra.CumulativeScores[p.ID] = 0
	}
	state.Extra = extra
	return beginHand(state, extra)
}

func beginHand(state *game.State, extra *State) error {
	extra.HandNumber++
	extra.LastHandResult = nil
	extra.Phase = PhaseAwaitingDraw
	extra.TakenUpcard = nil
	extra.TurnsThisHand = 0

	state.Deck = deck.New(deck.Standard())
	state.Deck.Shuffle()
	for _, p := range state.Players {
		cards, ok := state.Deck.DrawN(dealCount)
		if !ok {
			return errors.New("not enough cards to deal")
		}
		p.Cards = cards
	}
	upCard, ok := state.Deck.Draw()
	if !ok {
		return errors.New("not enough cards to start the discard pile")
	}
	state.Discard = deck.New([]deck.Card{upCard})

	// SetTurn copies the seat, never &extra.FirstActor: the engine holds the pointer
	// until it settles the cursor, and a write to FirstActor in that window would
	// silently redirect the turn.
	state.SetTurn(extra.FirstActor)
	return nil
}

var (
	errHandOver      = errors.New("the hand is over")
	errUnknownAction = errors.New("unknown action")
	errDrawFirst     = errors.New("must draw first")
	errDiscardFirst  = errors.New("must discard first")
)

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	if _, isNext := action.(ActionNextHand); isNext {
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return game.ValidateNextHand(extra.HandComplete(), extra.MatchComplete)
	}
	if extra.HandComplete() {
		return errHandOver
	}

	p := state.Players[state.CurrentTurn]
	switch action := action.(type) {
	case ActionDrawStock:
		if extra.Phase != PhaseAwaitingDraw {
			return errDiscardFirst
		}
		// wallStockSize cards stay undealt: drawing them would strand the hand.
		if state.Deck.Size() <= wallStockSize {
			return errors.New("stock is at the wall")
		}
		return nil
	case ActionDrawDiscard:
		if extra.Phase != PhaseAwaitingDraw {
			return errDiscardFirst
		}
		if _, ok := state.Discard.Peek(); !ok {
			return errors.New("discard pile is empty")
		}
		return nil
	case ActionDiscard:
		return validateDiscard(extra, p, action.Card)
	case ActionKnock:
		if err := validateDiscard(extra, p, action.Discard); err != nil {
			return err
		}
		remaining := deck.RemoveOne(p.Cards, action.Discard)
		_, _, deadwoodPts := bestMeldSplit(remaining)
		if deadwoodPts > knockThreshold {
			return fmt.Errorf("deadwood %d exceeds limit %d", deadwoodPts, knockThreshold)
		}
		return nil
	default:
		return errUnknownAction
	}
}

// validateDiscard is the check a discard and a knock share: the turn has had its draw,
// the card is in the hand, and it is not the upcard just taken.
func validateDiscard(extra *State, p *game.Player, card deck.Card) error {
	if extra.Phase != PhaseAwaitingDiscard {
		return errDrawFirst
	}
	if !slices.Contains(p.Cards, card) {
		return errors.New("you don't have that card")
	}
	if !mayDiscard(card, extra.TakenUpcard) {
		// The standard rule; see State.TakenUpcard.
		return errors.New("cannot discard the card you just took from the discard pile")
	}
	return nil
}

// mayDiscard reports whether card may go back on the pile this turn, forbidden being
// the upcard just taken, if any.
func mayDiscard(card deck.Card, forbidden *deck.Card) bool {
	return forbidden == nil || card != *forbidden
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	p := state.Players[state.CurrentTurn]

	switch action := action.(type) {
	case ActionDrawStock:
		// The phase only moves on a card that actually arrived; AfterAction turns a
		// failed draw into an error rather than letting the player discard a card
		// they never picked up.
		if drawn, ok := state.Deck.Draw(); ok {
			p.Cards = append(p.Cards, drawn)
			extra.TakenUpcard = nil
			extra.Phase = PhaseAwaitingDiscard
		}
		// Draw then discard is one turn: keep the cursor on this seat.
		state.OverrideTurn(state.CurrentTurn)
	case ActionDrawDiscard:
		if drawn, ok := state.Discard.Draw(); ok {
			p.Cards = append(p.Cards, drawn)
			extra.TakenUpcard = &drawn
			extra.Phase = PhaseAwaitingDiscard
		}
		state.OverrideTurn(state.CurrentTurn)
	case ActionDiscard:
		p.Cards = deck.RemoveOne(p.Cards, action.Card)
		state.Discard.Add(action.Card)
		extra.TakenUpcard = nil
		extra.TurnsThisHand++
		extra.Phase = PhaseAwaitingDraw
	case ActionKnock:
		applyKnock(state, extra, action)
	case ActionNextHand:
		// dealt in AfterAction
	}
	return nil
}

// otherSeat is the opponent's seat at a table of two.
func otherSeat(seat int) int { return 1 - seat }

func applyKnock(state *game.State, extra *State, action ActionKnock) {
	knocker := state.Players[state.CurrentTurn]
	opponent := state.Players[otherSeat(state.CurrentTurn)]

	result, remaining := computeKnockOutcome(knocker, opponent, action.Discard)
	knocker.Cards = remaining
	// The knock card leaves play with the pile it was laid on: the hand is over,
	// nobody may draw from it again, and beginHand deals from a fresh 52. So the card
	// vanishes from the record - acceptable, because HandResult already carries every
	// card that scored, and it is why conservation here is a per-hand property rather
	// than a per-match one.
	state.Discard = deck.New([]deck.Card{})
	extra.TakenUpcard = nil
	extra.CumulativeScores[result.Winner] += result.ScoreDelta

	// A knock is the only scoring event in the game and the one players argue about,
	// so both deadwood counts and the direction the points went are recorded.
	slog.Info("gin rummy knock",
		"hand", extra.HandNumber,
		"knocker", knocker.ID,
		"winner", result.Winner,
		"outcome", result.Outcome.String(),
		"knocker_deadwood", result.KnockerDeadwoodPoints,
		"opponent_deadwood", result.OpponentDeadwoodPoints,
		"laid_off", len(result.LaidOffCards),
		"delta", result.ScoreDelta)

	closeHand(state, extra, result)
}

// closeHand ends the hand on result, and either the match with it or the turn parked
// on the seat that acts first in the next hand, which alternates.
func closeHand(state *game.State, extra *State, result *HandResult) {
	extra.Phase = PhaseHandOver
	extra.LastHandResult = result

	if matchOver(extra) {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return
	}
	extra.FirstActor = otherSeat(extra.FirstActor)
	state.SetTurn(extra.FirstActor)
}

// computeKnockOutcome settles a knock by knocker, who lays down discard, against
// opponent's hand. It returns the result and the knocker's hand without the discard.
func computeKnockOutcome(knocker, opponent *game.Player, discard deck.Card) (*HandResult, []deck.Card) {
	remaining := deck.RemoveOne(knocker.Cards, discard)
	knockerMelds, knockerDW, knockerPts := bestMeldSplit(remaining)

	result := &HandResult{
		KnockerMelds:          knockerMelds,
		KnockerDeadwood:       knockerDW,
		KnockerDeadwoodPoints: knockerPts,
	}

	if knockerPts == 0 {
		// Gin blocks layoffs, so the opponent's best arrangement is the one with the
		// lowest raw deadwood: opponent scores that + bonus to the knocker.
		oppMelds, oppDW, oppPts := bestMeldSplit(opponent.Cards)
		result.Outcome = OutcomeGin
		result.OpponentMelds = oppMelds
		result.OpponentDeadwood = oppDW
		result.OpponentDeadwoodPoints = oppPts
		result.ScoreDelta = oppPts + ginBonus
		result.Winner = knocker.ID
		return result, remaining
	}

	// Layoffs are open, so the defender arranges for the lowest total *after* them.
	oppMelds, oppDW, _ := bestMeldSplitAgainst(opponent.Cards, knockerMelds)
	result.OpponentMelds = oppMelds

	remDW, laidOff := applyLayoffs(oppDW, knockerMelds)
	remPts := sumDeadwood(remDW)
	result.LaidOffCards = laidOff
	result.OpponentDeadwood = remDW
	result.OpponentDeadwoodPoints = remPts

	if remPts <= knockerPts {
		result.Outcome = OutcomeUndercut
		result.Winner = opponent.ID
		result.ScoreDelta = (knockerPts - remPts) + undercutBonus
	} else {
		result.Outcome = OutcomeKnock
		result.Winner = knocker.ID
		result.ScoreDelta = remPts - knockerPts
	}
	return result, remaining
}

// matchOver reports whether the match is settled: somebody crossed the target, or
// the table has played maxHands hands trying to. Without the cap the wall path
// redeals forever - a hand nobody knocks in scores nothing, so the target on its own
// is not a termination condition.
func matchOver(extra *State) bool {
	return game.AnyScoreAtLeast(extra.CumulativeScores, targetScore) || extra.HandNumber >= maxHands
}

func (r *Rules) AfterAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	switch action.(type) {
	case ActionNextHand:
		return beginHand(state, extra)
	case ActionDrawStock, ActionDrawDiscard:
		if extra.Phase != PhaseAwaitingDiscard {
			return errors.New("draw failed: the pile came up empty")
		}
	case ActionDiscard:
		if cause, hit := handHitTheWall(state, extra); hit {
			settleWall(state, extra, cause)
		}
	}
	return nil
}

// handHitTheWall reports whether the hand is out of road, and why. A stock that ran
// out and a table that spent a hundred turns trading the upcard are very different
// hands, and the cause is the only place they are told apart.
func handHitTheWall(state *game.State, extra *State) (cause string, hit bool) {
	switch {
	case state.Deck.Size() <= wallStockSize:
		return "stock exhausted", true
	case extra.TurnsThisHand >= maxHandTurns:
		return "turn limit", true
	default:
		return "", false
	}
}

// settleWall ends a hand nobody knocked in. Nothing scores, so the match can only end
// here on the hand cap - which is what stops a table that walls every time from
// dealing forever.
func settleWall(state *game.State, extra *State, cause string) {
	slog.Info("gin rummy hand walled",
		"hand", extra.HandNumber,
		"cause", cause,
		"turns", extra.TurnsThisHand,
		"stock", state.Deck.Size())

	closeHand(state, extra, &HandResult{Outcome: OutcomeWall})
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	extra, ok := state.Extra.(*State)
	return ok && extra.MatchComplete
}

// Standings ranks by cumulative score descending: gin rummy is a high-score-wins
// game and StandingsByScore sorts ascending, so the score is negated.
func (r *Rules) Standings(state *game.State) []*game.Player {
	if _, ok := state.Extra.(*State); !ok {
		return nil
	}
	return game.StandingsByScore(state.Players, func(p *game.Player) int {
		return -r.StandingScore(state, p)
	})
}

func (r *Rules) TimeoutAction(state *game.State) game.Action {
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	if extra.HandComplete() {
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	}
	if state.CurrentTurn < 0 || state.CurrentTurn >= len(state.Players) {
		return nil
	}
	p := state.Players[state.CurrentTurn]
	switch extra.Phase {
	case PhaseAwaitingDraw:
		if state.Deck.Size() > wallStockSize {
			return ActionDrawStock{}
		}
		if _, ok := state.Discard.Peek(); ok {
			return ActionDrawDiscard{}
		}
		// At the wall with an empty pile nothing is legal: the stock is off limits,
		// there is nothing to take, and a knock needs the card drawn first. AfterAction
		// ends a walled hand before anyone is asked to draw, so this is unreachable in
		// play - handing back a move the validator refuses is what would turn it into
		// a silent kick if it ever were reached.
		return nil
	case PhaseAwaitingDiscard:
		if card, ok := ginDiscard(p.Cards, extra.TakenUpcard); ok {
			return ActionKnock{Discard: card}
		}
		if card, ok := autoDiscard(p.Cards, extra.TakenUpcard); ok {
			return ActionDiscard{Card: card}
		}
	case PhaseHandOver:
	}
	return nil
}

// autoDiscard picks the move TimeoutAction plays for an absent player: the priciest
// deadwood card, skipping the one they may not discard back. It must return something
// ValidateAction accepts, or the engine re-arms and takes the seat on the next expiry.
func autoDiscard(hand []deck.Card, forbidden *deck.Card) (deck.Card, bool) {
	allowed := func(c deck.Card) bool { return mayDiscard(c, forbidden) }

	// Split the real hand, then choose among its deadwood: splitting a pre-filtered
	// hand would optimise melds the player does not actually hold.
	_, deadwood, _ := bestMeldSplit(hand)
	if shed := slices.DeleteFunc(deadwood, func(c deck.Card) bool { return !allowed(c) }); len(shed) > 0 {
		return highestPointCard(shed), true
	}
	// Gin, or every deadwood card is the one card they may not lay back: break a meld.
	if i := slices.IndexFunc(hand, allowed); i >= 0 {
		return hand[i], true
	}
	return deck.Card{}, false
}

// ginDiscard finds a legal discard that leaves no deadwood. Gin scores the bonus and
// cannot be undercut, so an absent player knocks on it rather than discarding it away.
// Every card is tried: the priciest-deadwood pick autoDiscard makes need not be one.
func ginDiscard(hand []deck.Card, forbidden *deck.Card) (deck.Card, bool) {
	for _, card := range hand {
		if !mayDiscard(card, forbidden) {
			continue
		}
		if _, _, pts := bestMeldSplit(deck.RemoveOne(hand, card)); pts == 0 {
			return card, true
		}
	}
	return deck.Card{}, false
}

// TurnDuration stretches the between-hands prompt. Zero everywhere else means
// "engine default", not "no clock".
func (r *Rules) TurnDuration(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok || !extra.HandComplete() {
		return 0
	}
	return handOverTurnDuration
}

// StandingScore is the value Standings sorted by. Only equality is read, so the
// descending order Standings applies does not need repeating here.
func (r *Rules) StandingScore(state *game.State, p *game.Player) int {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	return extra.CumulativeScores[p.ID]
}
