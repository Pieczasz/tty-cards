// Package hearts is four-handed Hearts played to 100 points: the three-card
// pass, trick play with broken hearts and the queen of spades, and shooting the moon.
package hearts

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// Rules implements Hearts. It is strictly four-handed, so a leave ends the match.
type Rules struct{}

var (
	_ game.Rules               = (*Rules)(nil)
	_ game.PlayerLeaveHandler  = (*Rules)(nil)
	_ game.TurnTimeoutHandler  = (*Rules)(nil)
	_ game.TurnDurationHandler = (*Rules)(nil)
	_ game.StandingScorer      = (*Rules)(nil)
)

func (r *Rules) MinPlayers() int { return playerCount }
func (r *Rules) MaxPlayers() int { return playerCount }

func (r *Rules) InitialDeck() []deck.Card { return deck.Standard() }
func (r *Rules) InitialDealCount() int    { return 0 }

// ActionPassCards hands exactly three cards to the seat PassDirection names.
type ActionPassCards struct {
	Cards []deck.Card
}

func (a ActionPassCards) Name() string { return "hearts.PassCards" }

// ActionPlayCard plays Card to the trick on the table.
type ActionPlayCard struct {
	Card deck.Card
}

func (a ActionPlayCard) Name() string { return "hearts.PlayCard" }

// ActionNextHand deals the next hand; only the incoming dealer may submit it.
type ActionNextHand struct{}

func (a ActionNextHand) Name() string { return "hearts.NextHand" }

func (r *Rules) OnGameStart(state *game.State) error {
	n := len(state.Players)
	if n != playerCount {
		return fmt.Errorf("hearts requires exactly %d players, got %d", playerCount, n)
	}
	extra := &State{
		CumulativeScores: make(map[string]int, n),
		HandPoints:       make(map[string]int, n),
		TrickCards:       make(map[string]deck.Card, n),
		TargetScore:      targetScore,
	}
	for _, p := range state.Players {
		extra.CumulativeScores[p.ID] = 0
	}
	state.Extra = extra
	return beginHand(state, extra, state.CurrentTurn)
}

func beginHand(state *game.State, extra *State, dealer int) error {
	resetHandState(extra)
	extra.HandNumber++
	extra.DealerIndex = dealer

	state.Deck = deck.New(deck.Standard())
	state.Deck.Shuffle()
	for _, p := range state.Players {
		cards, ok := state.Deck.DrawN(cardsPerHand)
		if !ok {
			return errNotEnoughCards
		}
		p.Cards = cards
	}

	extra.PassDirection = PassDirection((extra.HandNumber - 1) % passDirectionCount) //nolint:gosec // G115: a value in 0..3 always fits
	if extra.PassDirection == PassNone {
		extra.Phase = PhaseTrickPlay
		state.SetTurn(findTwoOfClubs(state))
		return nil
	}

	extra.Phase = PhasePassing
	extra.PendingPasses = make(map[string][]deck.Card, playerCount)
	state.SetTurn(dealer)
	return nil
}

var errNotEnoughCards = errors.New("not enough cards to deal")

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	if _, isNextHand := action.(ActionNextHand); isNextHand {
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return game.ValidateNextHand(extra.HandComplete(), extra.MatchComplete)
	}

	switch extra.Phase {
	case PhasePassing:
		return validatePass(state, extra, action)
	case PhaseTrickPlay:
		return validatePlay(state, extra, action)
	case PhaseHandOver:
	}
	return game.ErrHandOver
}

func validatePass(state *game.State, extra *State, action game.Action) error {
	a, ok := action.(ActionPassCards)
	if !ok {
		return errors.New("must pass cards during passing phase")
	}
	if len(a.Cards) != cardsToPass {
		return fmt.Errorf("must pass exactly %d cards", cardsToPass)
	}
	p := state.Players[state.CurrentTurn]
	if extra.passed(p.ID) {
		return errors.New("you already passed this hand")
	}
	seen := make(map[deck.Card]bool, cardsToPass)
	for _, c := range a.Cards {
		if seen[c] {
			return errors.New("duplicate card in pass")
		}
		seen[c] = true
		if !slices.Contains(p.Cards, c) {
			return errors.New("you don't have that card")
		}
	}
	return nil
}

func validatePlay(state *game.State, extra *State, action game.Action) error {
	a, ok := action.(ActionPlayCard)
	if !ok {
		return errors.New("must play a card during trick play")
	}
	return validatePlayCard(extra, state.Players[state.CurrentTurn], a.Card)
}

func validatePlayCard(extra *State, p *game.Player, card deck.Card) error {
	if !slices.Contains(p.Cards, card) {
		return errors.New("you don't have that card")
	}

	leading := extra.leadingTrick()
	firstTrick := extra.TricksPlayed == 0
	// Both first-trick rules have an exemption for the hand that has nothing else:
	// a player holding only hearts may lead one, and a player holding only points
	// must be allowed to play one on trick 1.
	openingLead := leading && firstTrick
	unbrokenHeartLead := leading && card.Suit == deck.Hearts &&
		!extra.HeartsBroken && !onlyHearts(p.Cards)
	revoke := !leading && card.Suit != extra.LedSuit && handHasSuit(p.Cards, extra.LedSuit)
	dumpingPointsOnTrickOne := firstTrick && isPenaltyCard(card) && hasNonPenaltyCard(p.Cards)

	switch {
	case openingLead && card != twoOfClubs:
		return errors.New("must lead the 2 of clubs on trick 1")
	case unbrokenHeartLead:
		return errors.New("hearts have not been broken yet")
	case revoke:
		return errors.New("must follow suit")
	case dumpingPointsOnTrickOne:
		return errors.New("cannot play a point card on trick 1")
	default:
		return nil
	}
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	switch a := action.(type) {
	case ActionPassCards:
		p := state.Players[state.CurrentTurn]
		p.Cards = deck.RemoveEach(p.Cards, a.Cards)
		// Cloned: these cards sit in engine state for up to three turns before
		// applyAllPasses delivers them, and a caller that retains its slice must
		// not be able to rewrite another seat's incoming cards.
		extra.PendingPasses[p.ID] = slices.Clone(a.Cards)
	case ActionPlayCard:
		p := state.Players[state.CurrentTurn]
		extra.startTrick()
		p.Cards = deck.RemoveOne(p.Cards, a.Card)
		if len(extra.TrickCards) == 0 {
			extra.LedSuit = a.Card.Suit
			extra.TrickLeader = state.CurrentTurn
		}
		extra.TrickCards[p.ID] = a.Card
		if a.Card.Suit == deck.Hearts {
			extra.HeartsBroken = true
		}
	case ActionNextHand:
		// dealt in AfterAction
	}
	return nil
}

func (r *Rules) AfterAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	switch action.(type) {
	case ActionPassCards:
		afterPass(state, extra)
	case ActionPlayCard:
		afterPlay(state, extra)
	case ActionNextHand:
		return beginHand(state, extra, state.CurrentTurn)
	}
	return nil
}

func afterPass(state *game.State, extra *State) {
	if len(extra.PendingPasses) < len(state.Players) {
		state.OverrideTurn(nextUnpassedSeat(state, extra, state.CurrentTurn))
		return
	}
	applyAllPasses(state, extra)
	extra.Phase = PhaseTrickPlay
	extra.PendingPasses = nil
	state.SetTurn(findTwoOfClubs(state))
}

func afterPlay(state *game.State, extra *State) {
	if len(extra.TrickCards) < len(state.Players) {
		return
	}

	winnerSeat := trickWinner(state, extra)
	winnerID := state.Players[winnerSeat].ID
	extra.HandPoints[winnerID] += trickPoints(extra.TrickCards)
	extra.LastTrickWinner = winnerID
	extra.TricksPlayed++
	// The cards stay on the table: the engine broadcasts this play next, and a
	// trick swept here would never be seen by the three players who did not take it.
	extra.TrickComplete = true

	if extra.TricksPlayed < cardsPerHand {
		state.SetTurn(winnerSeat)
		return
	}

	scoreHand(extra, state.Players)
	extra.Phase = PhaseHandOver

	if game.AnyScoreAtLeast(extra.CumulativeScores, extra.TargetScore) {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return
	}

	state.SetTurn(game.SeatAt(extra.DealerIndex+1, len(state.Players)))
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	extra, ok := state.Extra.(*State)
	return ok && extra.MatchComplete
}

// Standings ranks by cumulative score ascending: hearts is a low-score-wins game,
// so the seat that took the fewest points is first.
func (r *Rules) Standings(state *game.State) []*game.Player {
	if _, ok := state.Extra.(*State); !ok {
		return nil
	}
	return game.StandingsByScore(state.Players, func(p *game.Player) int {
		return r.StandingScore(state, p)
	})
}

func (r *Rules) TimeoutAction(state *game.State) game.Action {
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	switch extra.Phase {
	case PhaseHandOver:
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	case PhasePassing:
		p := state.Players[state.CurrentTurn]
		return ActionPassCards{Cards: threeMostDangerous(p.Cards)}
	case PhaseTrickPlay:
		p := state.Players[state.CurrentTurn]
		if card, ok := firstLegalCard(extra, p); ok {
			return ActionPlayCard{Card: card}
		}
	}
	return nil
}

func (r *Rules) TurnDuration(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	switch extra.Phase {
	case PhasePassing:
		return passTurnDuration
	case PhaseHandOver:
		// The between-hands prompt is a decision, not a play, and it needs longer
		// than a turn. Every other phase returns zero, which means "engine default"
		// rather than "no clock".
		return handOverTurnDuration
	case PhaseTrickPlay:
	}
	return 0
}

// OnPlayerLeave ends the match: hearts does not play three-handed. Interrupted tells
// the engine, and through it finalize, that the seats still playing did not finish it.
func (r *Rules) OnPlayerLeave(state *game.State, _ string) {
	if extra, ok := state.Extra.(*State); ok {
		extra.MatchComplete = true
		state.Interrupted = true
	}
}

func (r *Rules) AfterPlayerRemoved(_ *game.State, _ int) {}

// StandingScore is the value Standings sorted by, so players who finished the match
// on the same total are reported as the draw they are. Mid-hand - a table that ended
// on a disconnect - the live hand's points count too, or the winner is whoever sat
// first. Once scoreHand has folded them into the totals they must not count twice.
func (r *Rules) StandingScore(state *game.State, p *game.Player) int {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	if extra.HandComplete() {
		return extra.CumulativeScores[p.ID]
	}
	return extra.CumulativeScores[p.ID] + extra.HandPoints[p.ID]
}
