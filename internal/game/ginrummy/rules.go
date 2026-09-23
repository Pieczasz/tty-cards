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

type ActionDrawStock struct{}

func (a ActionDrawStock) Name() string { return "ginrummy.DrawStock" }

type ActionDrawDiscard struct{}

func (a ActionDrawDiscard) Name() string { return "ginrummy.DrawDiscard" }

type ActionDiscard struct {
	Card deck.Card
}

func (a ActionDiscard) Name() string { return "ginrummy.Discard" }

type ActionKnock struct {
	Discard deck.Card
}

func (a ActionKnock) Name() string { return "ginrummy.Knock" }

type ActionNextHand struct{}

func (a ActionNextHand) Name() string { return "ginrummy.NextHand" }

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 2 }

func (r *Rules) InitialDeck() []deck.Card { return deck.StandardDeck() }

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
	return r.beginHand(state, extra)
}

func (r *Rules) beginHand(state *game.State, extra *State) error {
	extra.HandNumber++
	extra.HandComplete = false
	extra.LastHandResult = nil
	extra.HandPhase = AwaitingDraw
	extra.TakenUpcard = nil
	extra.TurnsThisHand = 0

	state.Deck = deck.New(deck.StandardDeck())
	state.Deck.Shuffle()
	for _, p := range state.Players {
		cards, ok := state.Deck.DrawNCards(dealCount)
		if !ok {
			return errors.New("insufficient cards to deal")
		}
		p.Cards = cards
	}
	upCard, ok := state.Deck.Draw()
	if !ok {
		return errors.New("not enough cards to start discard pile")
	}
	state.Discard = deck.New([]deck.Card{upCard})

	// A local, never &extra.FirstActor: the engine holds this pointer until it settles
	// the cursor, and a write to FirstActor in that window would silently redirect the turn.
	first := extra.FirstActor
	state.CurrentTurn = first
	state.OverrideNextTurn = &first
	return nil
}

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	if _, isNext := action.(ActionNextHand); isNext {
		if !extra.HandComplete {
			return errors.New("hand is still being played")
		}
		if extra.MatchComplete {
			return errors.New("match is over")
		}
		return nil
	}
	if extra.HandComplete {
		return errors.New("hand is over")
	}

	p := state.Players[state.CurrentTurn]
	switch action := action.(type) {
	case ActionDrawStock:
		if extra.HandPhase != AwaitingDraw {
			return errors.New("must discard first")
		}
		// wallStockSize cards stay undealt: drawing them would strand the hand.
		if state.Deck.Size() <= wallStockSize {
			return errors.New("stock is at the wall")
		}
		return nil
	case ActionDrawDiscard:
		if extra.HandPhase != AwaitingDraw {
			return errors.New("must discard first")
		}
		if _, ok := state.Discard.Peek(); !ok {
			return errors.New("discard pile is empty")
		}
		return nil
	case ActionDiscard:
		if extra.HandPhase != AwaitingDiscard {
			return errors.New("must draw first")
		}
		if !slices.Contains(p.Cards, action.Card) {
			return errors.New("you don't have that card")
		}
		return validateNotTakenUpcard(extra, action.Card)
	case ActionKnock:
		if extra.HandPhase != AwaitingDiscard {
			return errors.New("must draw first")
		}
		if !slices.Contains(p.Cards, action.Discard) {
			return errors.New("you don't have that card")
		}
		if err := validateNotTakenUpcard(extra, action.Discard); err != nil {
			return err
		}
		remaining := deck.RemoveOne(p.Cards, action.Discard)
		_, _, deadwoodPts := bestMeldSplit(remaining)
		if deadwoodPts > knockThreshold {
			return fmt.Errorf("deadwood %d exceeds limit %d", deadwoodPts, knockThreshold)
		}
		return nil
	default:
		return errors.New("unknown action")
	}
}

// validateNotTakenUpcard enforces the standard rule that the card just drawn from
// the discard pile cannot be laid straight back. See State.TakenUpcard.
func validateNotTakenUpcard(extra *State, card deck.Card) error {
	if extra.TakenUpcard != nil && *extra.TakenUpcard == card {
		return errors.New("cannot discard the card you just took from the discard pile")
	}
	return nil
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
			extra.HandPhase = AwaitingDiscard
		}
		// Draw then discard is one turn: keep the cursor on this seat.
		cur := state.CurrentTurn
		state.OverrideNextTurn = &cur
	case ActionDrawDiscard:
		if drawn, ok := state.Discard.Draw(); ok {
			p.Cards = append(p.Cards, drawn)
			extra.TakenUpcard = &drawn
			extra.HandPhase = AwaitingDiscard
		}
		cur := state.CurrentTurn
		state.OverrideNextTurn = &cur
	case ActionDiscard:
		p.Cards = deck.RemoveOne(p.Cards, action.Card)
		state.Discard.AddCard(action.Card)
		extra.TakenUpcard = nil
		extra.TurnsThisHand++
		extra.HandPhase = AwaitingDraw
	case ActionKnock:
		r.applyKnock(state, extra, action)
	case ActionNextHand:
		// dealt in AfterAction
	}
	return nil
}

func (r *Rules) applyKnock(state *game.State, extra *State, action ActionKnock) {
	knockerID := state.Players[state.CurrentTurn].ID
	opponentID := state.Players[1-state.CurrentTurn].ID

	knockerHand := state.Players[state.CurrentTurn].Cards
	opponentHand := state.Players[1-state.CurrentTurn].Cards

	result, remaining := computeKnockOutcome(knockerID, opponentID, knockerHand, opponentHand, action.Discard)

	state.Players[state.CurrentTurn].Cards = remaining
	// The knock card leaves play with the pile it was laid on: the hand is over,
	// nobody may draw from it again, and beginHand deals from a fresh 52. So the card
	// vanishes from the record - acceptable, because HandResult already carries every
	// card that scored, and it is why conservation here is a per-hand property rather
	// than a per-match one.
	state.Discard = deck.New([]deck.Card{})
	extra.TakenUpcard = nil
	extra.HandPhase = HandOver
	extra.HandComplete = true
	extra.LastHandResult = result

	extra.CumulativeScores[result.Winner] += result.ScoreDelta

	// A knock is the only scoring event in the game and the one players argue about,
	// so both deadwood counts and the direction the points went are recorded.
	slog.Info("gin rummy knock",
		"hand", extra.HandNumber,
		"knocker", knockerID,
		"winner", result.Winner,
		"gin", result.Gin,
		"undercut", result.Undercut,
		"knocker_deadwood", result.KnockerDeadwoodPoints,
		"opponent_deadwood", result.OpponentDeadwoodPoints,
		"laid_off", len(result.LaidOffCards),
		"delta", result.ScoreDelta)

	if matchOver(extra) {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return
	}

	nextFirst := 1 - extra.FirstActor
	extra.FirstActor = nextFirst
	state.CurrentTurn = nextFirst
	state.OverrideNextTurn = &nextFirst
}

func computeKnockOutcome(
	knockerID, opponentID string,
	knockerHand, opponentHand []deck.Card,
	discard deck.Card,
) (*HandResult, []deck.Card) {
	remaining := deck.RemoveOne(knockerHand, discard)
	knockerMelds, knockerDW, knockerPts := bestMeldSplit(remaining)

	result := &HandResult{
		KnockerMelds:          knockerMelds,
		KnockerDeadwood:       knockerDW,
		KnockerDeadwoodPoints: knockerPts,
		Gin:                   knockerPts == 0,
	}

	if result.Gin {
		// Gin blocks layoffs, so the opponent's best arrangement is the one with the
		// lowest raw deadwood: opponent scores that + bonus to the knocker.
		oppMelds, oppDW, oppPts := bestMeldSplit(opponentHand)
		result.OpponentMelds = oppMelds
		result.OpponentDeadwood = oppDW
		result.OpponentDeadwoodPoints = oppPts
		result.ScoreDelta = oppPts + ginBonus
		result.Winner = knockerID
		return result, remaining
	}

	// Layoffs are open, so the defender arranges for the lowest total *after* them.
	oppMelds, oppDW, _ := bestMeldSplitAgainst(opponentHand, knockerMelds)
	result.OpponentMelds = oppMelds

	remDW, laidOff := applyLayoffs(oppDW, knockerMelds)
	remPts := sumDeadwood(remDW)
	result.LaidOffCards = laidOff
	result.OpponentDeadwood = remDW
	result.OpponentDeadwoodPoints = remPts

	if remPts <= knockerPts {
		result.Undercut = true
		result.Winner = opponentID
		result.ScoreDelta = (knockerPts - remPts) + undercutBonus
	} else {
		result.Winner = knockerID
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
		return r.beginHand(state, extra)
	case ActionDrawStock, ActionDrawDiscard:
		if extra.HandPhase != AwaitingDiscard {
			return errors.New("draw failed: the pile came up empty")
		}
	case ActionDiscard:
		if cause, hit := handHitTheWall(state, extra); hit {
			r.settleWall(state, extra, cause)
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
func (r *Rules) settleWall(state *game.State, extra *State, cause string) {
	slog.Info("gin rummy hand walled",
		"hand", extra.HandNumber,
		"cause", cause,
		"turns", extra.TurnsThisHand,
		"stock", state.Deck.Size())

	extra.HandComplete = true
	extra.HandPhase = HandOver
	extra.LastHandResult = &HandResult{Wall: true}

	if matchOver(extra) {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return
	}

	nextFirst := 1 - extra.FirstActor
	extra.FirstActor = nextFirst
	state.CurrentTurn = nextFirst
	state.OverrideNextTurn = &nextFirst
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
	if extra.HandComplete {
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	}
	if state.CurrentTurn < 0 || state.CurrentTurn >= len(state.Players) {
		return nil
	}
	p := state.Players[state.CurrentTurn]
	switch extra.HandPhase {
	case AwaitingDraw:
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
	case AwaitingDiscard:
		card, ok := autoDiscard(p.Cards, extra.TakenUpcard)
		if !ok {
			return nil
		}
		return ActionDiscard{Card: card}
	default:
		return nil
	}
}

// autoDiscard picks the move TimeoutAction plays for an absent player: the priciest
// deadwood card, skipping the one they may not discard back. It must return something
// ValidateAction accepts, or the engine re-arms and takes the seat on the next expiry.
func autoDiscard(hand []deck.Card, forbidden *deck.Card) (deck.Card, bool) {
	allowed := func(c deck.Card) bool { return forbidden == nil || c != *forbidden }

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

// TurnTimeout stretches the between-hands prompt. Zero everywhere else means
// "engine default", not "no clock".
func (r *Rules) TurnTimeout(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok || !extra.HandComplete {
		return 0
	}
	return handOverTimeout
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
