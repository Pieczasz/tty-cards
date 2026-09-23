package hearts

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

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

func (r *Rules) InitialDeck() []deck.Card { return deck.StandardDeck() }
func (r *Rules) InitialDealCount() int    { return 0 }

type ActionPassCards struct {
	Cards []deck.Card
}

func (a ActionPassCards) Name() string { return "hearts.PassCards" }

type ActionPlayCard struct {
	Card deck.Card
}

func (a ActionPlayCard) Name() string { return "hearts.PlayCard" }

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
		TargetScore:      DefaultTargetScore,
	}
	for _, p := range state.Players {
		extra.CumulativeScores[p.ID] = 0
	}
	state.Extra = extra
	return r.beginHand(state, extra, state.CurrentTurn)
}

func (r *Rules) beginHand(state *game.State, extra *State, dealer int) error {
	resetHandState(extra)
	extra.HandNumber++
	extra.DealerIndex = dealer

	state.Deck = deck.New(deck.StandardDeck())
	state.Deck.Shuffle()
	for _, p := range state.Players {
		cards, ok := state.Deck.DrawNCards(cardsPerHand)
		if !ok {
			return errors.New("not enough cards to deal")
		}
		p.Cards = cards
	}

	extra.PassDirection = PassDirection((extra.HandNumber - 1) % 4) //nolint:gosec // G115: a value in 0..3 always fits
	if extra.PassDirection == PassNone {
		extra.Stage = StageTrickPlay
		leader := findTwoOfClubs(state)
		state.CurrentTurn = leader
		state.OverrideNextTurn = &leader
		return nil
	}

	extra.Stage = StagePassing
	extra.PendingPasses = make(map[string][]deck.Card, playerCount)
	extra.Passed = make(map[string]bool, playerCount)
	state.CurrentTurn = dealer
	state.OverrideNextTurn = &dealer
	return nil
}

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}

	if _, isNextHand := action.(ActionNextHand); isNextHand {
		if extra.Stage != StageHandOver {
			return errors.New("the hand is still being played")
		}
		if extra.MatchComplete {
			return errors.New("the match is over")
		}
		return nil
	}

	switch extra.Stage {
	case StagePassing:
		return validatePass(state, extra, action)
	case StageTrickPlay:
		return validatePlay(state, extra, action)
	default:
		return errors.New("hand is over")
	}
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
	if extra.Passed[p.ID] {
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
		extra.Passed[p.ID] = true
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
		return r.afterPass(state, extra)
	case ActionPlayCard:
		return r.afterPlay(state, extra)
	case ActionNextHand:
		return r.beginHand(state, extra, state.CurrentTurn)
	}
	return nil
}

func (r *Rules) afterPass(state *game.State, extra *State) error {
	if len(extra.Passed) < len(state.Players) {
		next := nextUnpassedSeat(state, extra, state.CurrentTurn)
		state.OverrideNextTurn = &next
		return nil
	}
	applyAllPasses(state, extra)
	extra.Stage = StageTrickPlay
	extra.PendingPasses = nil
	extra.Passed = nil
	leader := findTwoOfClubs(state)
	state.CurrentTurn = leader
	state.OverrideNextTurn = &leader
	return nil
}

func (r *Rules) afterPlay(state *game.State, extra *State) error {
	if len(extra.TrickCards) < len(state.Players) {
		return nil
	}

	winnerID, winnerSeat := trickWinner(state, extra)
	extra.HandPoints[winnerID] += trickPoints(extra.TrickCards)
	extra.LastTrickWinner = winnerID
	extra.TricksPlayed++
	// The cards stay on the table: the engine broadcasts this play next, and a
	// trick swept here would never be seen by the three players who did not take it.
	extra.TrickComplete = true

	if extra.TricksPlayed < cardsPerHand {
		state.CurrentTurn = winnerSeat
		state.OverrideNextTurn = &winnerSeat
		return nil
	}

	scoreHand(extra, state.Players)
	extra.Stage = StageHandOver
	extra.HandComplete = true

	if game.AnyScoreAtLeast(extra.CumulativeScores, extra.TargetScore) {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return nil
	}

	nextDealer := (extra.DealerIndex + 1) % len(state.Players)
	state.CurrentTurn = nextDealer
	state.OverrideNextTurn = &nextDealer
	return nil
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
	switch extra.Stage {
	case StageHandOver:
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	case StagePassing:
		p := state.Players[state.CurrentTurn]
		return ActionPassCards{Cards: threeMostDangerous(p.Cards)}
	case StageTrickPlay:
		p := state.Players[state.CurrentTurn]
		if card, ok := firstLegalCard(extra, p); ok {
			return ActionPlayCard{Card: card}
		}
		return nil
	default:
		return nil
	}
}

func (r *Rules) TurnTimeout(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	switch extra.Stage {
	case StagePassing:
		return passTurnTimeout
	case StageHandOver:
		// The between-hands prompt is a decision, not a play, and it needs longer
		// than a turn. Every other stage returns zero, which means "engine default"
		// rather than "no clock".
		return handOverTurnTimeout
	default:
		return 0
	}
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
	if extra.HandComplete {
		return extra.CumulativeScores[p.ID]
	}
	return extra.CumulativeScores[p.ID] + extra.HandPoints[p.ID]
}
