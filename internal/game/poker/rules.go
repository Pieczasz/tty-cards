package poker

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

const (
	DefaultStack      uint = 1000
	DefaultSmallBlind uint = 25
	DefaultBigBlind   uint = 50
	// HandsPerMatch is how many hands a match runs for. Chips carry across them
	// and the biggest stack at the end wins, so one cold hand costs a player
	// position rather than the whole match.
	HandsPerMatch = 10
)

// Rules implements No-Limit Texas Hold'em over a HandsPerMatch-hand match.
type Rules struct{}

var (
	_ game.Rules               = (*Rules)(nil)
	_ game.PlayerLeaveHandler  = (*Rules)(nil)
	_ game.TurnTimeoutHandler  = (*Rules)(nil)
	_ game.TurnDurationHandler = (*Rules)(nil)
	// Without it, deleting StandingScore still compiles and the engine silently
	// splits every draw by seat order.
	_ game.StandingScorer = (*Rules)(nil)
)

// TimeoutAction never risks chips on an absent player's behalf: it checks when that
// is free and folds when it is not, which is what every real poker client does with
// a player who has stopped responding. A call is also free when no opponent can put
// in more than the player already has out: everything past that is refunded at
// showdown, and folding would forfeit a bet the player had already covered.
//
// Between hands it deals the next one instead. The player holding the button is the
// only one who can, so an absent dealer would otherwise freeze the match for
// everyone still playing.
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
	if ToCall(extra, p.ID) == 0 {
		return ActionCheck{}
	}
	if largestCallableBet(state, extra, p) <= extra.PlayerBets[p.ID] {
		return ActionCall{}
	}
	return ActionFold{}
}

// dealTurnTimeout is how long the incoming dealer has to start the next hand. Dealing
// is a decision about whether to keep playing rather than a move made under pressure,
// so it gets longer than a betting turn - but it stays bounded, because an absent
// dealer is the one seat that can freeze the match for everybody else.
const dealTurnTimeout = time.Minute

// TurnTimeout gives the between-hands deal its own clock and leaves every betting
// turn on the engine's.
func (r *Rules) TurnTimeout(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok || !extra.HandComplete || extra.MatchComplete {
		return 0
	}
	return dealTurnTimeout
}

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 9 }

func (r *Rules) InitialDeck() []deck.Card {
	return deck.StandardDeck()
}

// holeCards is what each funded seat is dealt at the start of a hand.
const holeCards = 2

// InitialDealCount is zero because a match deals a fresh hand every round, not
// once at the start: beginHand owns the deal so there is a single code path for
// it, hand one included.
func (r *Rules) InitialDealCount() int {
	return 0
}

// minDeckAfterDeal is burn+flop+turn+river plus a two-card margin, checked once
// the hole cards for the hand are out.
const minDeckAfterDeal = 1 + 3 + 1 + 1 + 2

func (r *Rules) OnGameStart(state *game.State) error {
	nPlayers := len(state.Players)
	if nPlayers == 0 {
		return errors.New("cannot start a hand with no players")
	}

	extra := &State{
		// The engine seats the first turn at random; that seat takes the button.
		DealerIndex:      state.CurrentTurn,
		SmallBlind:       DefaultSmallBlind,
		BigBlind:         DefaultBigBlind,
		HandsTotal:       HandsPerMatch,
		Folded:           make(map[string]bool, nPlayers),
		PlayersAllIn:     make(map[string]bool, nPlayers),
		Table:            make([]deck.Card, 0, 5),
		PlayerChips:      make(map[string]uint, nPlayers),
		PlayerBets:       make(map[string]uint, nPlayers),
		TotalContributed: make(map[string]uint, nPlayers),
		ActedThisRound:   make(map[string]bool, nPlayers),
		LastBetLevel:     make(map[string]uint, nPlayers),
	}
	for _, p := range state.Players {
		extra.PlayerChips[p.ID] = DefaultStack
	}
	state.Extra = extra

	return r.beginHandOrFinish(state, extra, extra.DealerIndex)
}

// beginHandOrFinish deals a hand and closes it out when the deal already finished
// it: blinds big enough to put every funded seat all-in run the board out inside
// beginHand, before anybody acts. Both the opening deal and every ActionNextHand go
// through here, because a hand left complete but unfinished parks nobody on turn
// and the match stops on a result screen nobody can dismiss.
func (r *Rules) beginHandOrFinish(state *game.State, extra *State, dealer int) error {
	if err := r.beginHand(state, extra, dealer); err != nil {
		return err
	}
	if extra.HandComplete {
		finishHand(state, extra)
	}
	return nil
}

// beginHand deals the next hand of the match: fresh shuffled deck, hole cards for
// everyone still holding chips, button and blinds moved on. A busted player is
// marked folded for the rest of the match so the turn cursor skips their seat.
func (r *Rules) beginHand(state *game.State, extra *State, dealer int) error {
	resetForHand(state, extra)
	extra.HandNumber++
	extra.handStartChips = chipsInPlay(extra)

	state.Deck = deck.New(r.InitialDeck())
	state.Deck.Shuffle()
	if err := dealHoleCards(state, extra, holeCards); err != nil {
		return err
	}
	if state.Deck.Size() < minDeckAfterDeal {
		slog.Error("poker deck too small to run the board",
			"hand", extra.HandNumber, "size", state.Deck.Size(), "want", minDeckAfterDeal)
		return errors.New("not enough cards to run the board")
	}

	// Seats are counted before the blinds are posted: a blind big enough to bust a
	// short stack would otherwise make a full table look heads-up.
	headsUp := fundedSeats(state, extra) == 2
	setBlinds(state, extra, dealer, headsUp)
	postBlind(extra, state.Players[extra.SBIndex], extra.SmallBlind)
	postBlind(extra, state.Players[extra.BBIndex], extra.BigBlind)
	// A blind too short to post in full is all-in for less; the bring-in stays at the
	// full big blind and the shortfall is dead money, so the opening bet is the blind
	// rather than what was actually posted. Following the posted amount would drop the
	// opening bet below the blind and, since MinRaise is measured from it, drag the
	// first legal raise under a full blind with it.
	extra.CurrentBet = extra.BigBlind

	first := firstToActPreflop(state, extra, headsUp)
	if first < 0 {
		// Every funded player was put all-in by their own blind: nobody can act,
		// so the board just runs out. The blinds are already in the pool, so a
		// run-out that fails hands them back rather than stranding them.
		return settleOrUnwind(state, extra)
	}
	state.CurrentTurn = first
	state.OverrideNextTurn = &first
	return nil
}

func resetForHand(state *game.State, extra *State) {
	clear(extra.Folded)
	clear(extra.PlayersAllIn)
	clear(extra.PlayerBets)
	clear(extra.TotalContributed)
	clear(extra.ActedThisRound)
	clear(extra.LastBetLevel)
	extra.Table = extra.Table[:0]
	extra.Pots = nil
	extra.Winners = nil
	extra.MainPool = 0
	extra.CurrentBet = 0
	extra.MinRaise = extra.BigBlind
	extra.Phase = PreFlop
	extra.HandComplete = false
	extra.ReachedShowdown = false
	state.Winner = nil
}

func dealHoleCards(state *game.State, extra *State, count int) error {
	funded := 0
	for _, p := range state.Players {
		if extra.PlayerChips[p.ID] == 0 {
			p.Cards = nil
			extra.Folded[p.ID] = true
			extra.ActedThisRound[p.ID] = true
			continue
		}
		cards, ok := state.Deck.DrawNCards(count)
		if !ok {
			slog.Error("poker deck empty dealing hole cards",
				"hand", extra.HandNumber, "player", p.ID, "dealt", funded)
			return errors.New("insufficient number of cards to deal for all players")
		}
		p.Cards = cards
		funded++
	}
	if funded < 2 {
		slog.Error("poker cannot deal a hand", "hand", extra.HandNumber, "funded", funded)
		return errors.New("not enough funded players to deal a hand")
	}
	return nil
}

// setBlinds puts the button on dealer and derives the blinds from it. Heads-up
// posts the small blind on the button.
func setBlinds(state *game.State, extra *State, dealer int, headsUp bool) {
	extra.DealerIndex = dealer
	if headsUp {
		extra.SBIndex = dealer
		extra.BBIndex = nextFundedSeat(state, extra, dealer)
		return
	}
	extra.SBIndex = nextFundedSeat(state, extra, dealer)
	extra.BBIndex = nextFundedSeat(state, extra, extra.SBIndex)
}

// firstToActPreflop returns the seat under the gun, skipping anyone the blinds
// already put all-in. -1 means nobody at the table can act.
func firstToActPreflop(state *game.State, extra *State, headsUp bool) int {
	// Heads-up the button acts first, so its own seat has to be considered; every
	// other table starts with the seat after the big blind.
	if headsUp && !cannotAct(extra, state.Players[extra.DealerIndex].ID) {
		return extra.DealerIndex
	}
	return nextToAct(state, extra, extra.BBIndex)
}

// recordBustouts stamps the hand each newly broke player went out on, so final
// standings can order them by how long they lasted.
func recordBustouts(state *game.State, extra *State) {
	for _, p := range state.Players {
		if extra.PlayerChips[p.ID] > 0 {
			continue
		}
		if extra.BustedAtHand == nil {
			extra.BustedAtHand = make(map[string]int, len(state.Players))
		}
		if _, done := extra.BustedAtHand[p.ID]; !done {
			extra.BustedAtHand[p.ID] = extra.HandNumber
		}
	}
}

func fundedSeats(state *game.State, extra *State) int {
	n := 0
	for _, p := range state.Players {
		if extra.PlayerChips[p.ID] > 0 {
			n++
		}
	}
	return n
}

func nextFundedSeat(state *game.State, extra *State, from int) int {
	idx := nextSeat(from, len(state.Players), func(idx int) bool {
		return extra.PlayerChips[state.Players[idx].ID] > 0
	})
	if idx < 0 {
		return from
	}
	return idx
}

// finishHand closes out a hand. It ends the match once the hands run out or only
// one player still has chips; otherwise it parks the turn on the next dealer, who
// deals the following hand with ActionNextHand.
func finishHand(state *game.State, extra *State) {
	extra.HandComplete = true
	extra.Phase = Showdown
	checkChipConservation(extra)
	recordBustouts(state, extra)
	if extra.HandNumber >= extra.HandsTotal || fundedSeats(state, extra) <= 1 {
		extra.MatchComplete = true
		state.OverrideNextTurn = nil
		return
	}
	next := nextFundedSeat(state, extra, extra.DealerIndex)
	state.CurrentTurn = next
	state.OverrideNextTurn = &next
}

// checkChipConservation is a money-bug tripwire. Chips only ever move between a stack
// and the pool, so once a hand is closed out the two together must still add up to
// what the table had when the hand was dealt, and every one of them must be back in a
// stack. A mismatch means a pot paid out more or less than it collected, which is
// worth a log line even though it is too late to fix.
//
// The pool is checked separately because the total cannot see it: chipsInPlay counts
// MainPool, so a hand that ends without paying a pot out balances, and the next
// resetForHand quietly zeroes the stranded chips.
func checkChipConservation(extra *State) {
	if extra.MainPool != 0 {
		slog.Error("poker hand finished with chips still in the pot",
			"hand", extra.HandNumber,
			"phase", extra.Phase.String(),
			"pool", extra.MainPool,
		)
	}
	if extra.handStartChips == 0 {
		return // a hand-shaped State assembled by hand, not dealt by beginHand
	}
	total := chipsInPlay(extra)
	if total == extra.handStartChips {
		return
	}
	slog.Error("poker chip conservation broken",
		"hand", extra.HandNumber,
		"phase", extra.Phase.String(),
		"delta", int64(total)-int64(extra.handStartChips), //nolint:gosec // G115: chip totals are far below 2^63
	)
}

func postBlind(extra *State, p *game.Player, amount uint) {
	pay := min(amount, extra.PlayerChips[p.ID])
	extra.PlayerChips[p.ID] -= pay
	extra.PlayerBets[p.ID] += pay
	extra.TotalContributed[p.ID] += pay
	extra.MainPool += pay
	if extra.PlayerChips[p.ID] == 0 {
		extra.PlayersAllIn[p.ID] = true
	}
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	extra, ok := state.Extra.(*State)
	if !ok {
		return false
	}
	return extra.MatchComplete
}

func (r *Rules) Standings(state *game.State) []*game.Player {
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	return rankPlayers(state, extra)
}

// StandingScore is the group a player lands in once resultLevel stops separating
// them - two players who busted on the same hand are a draw, not places i and i+1
// split by whose ID sorts first. Only equality is read, so the group index is enough;
// chips alone would not be, since it would tie two busts from different hands.
func (r *Rules) StandingScore(state *game.State, p *game.Player) int {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	ranked := rankPlayers(state, extra)
	level := resultLevel(state, extra)
	group := 0
	for i, q := range ranked {
		if i > 0 && level(ranked[i-1], q) != 0 {
			group++
		}
		if q.ID == p.ID {
			return group
		}
	}
	return group
}

// OnPlayerLeave folds the departing player. Turn and seat resolution runs in
// AfterPlayerRemoved, once the seats have actually shifted.
//
// An all-in player is left alone: they have no decisions left to make, so folding
// them would forfeit chips they had no way to protect. They stay in the pot they are
// committed to (see contenders) and are shown down like anybody else.
func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra, ok := state.Extra.(*State)
	if !ok || extra.HandComplete || extra.PlayersAllIn[playerID] {
		return
	}
	extra.Folded[playerID] = true
	extra.ActedThisRound[playerID] = true
}

// AfterPlayerRemoved reindexes the button/blinds and picks the next actor from
// post-removal seats, so a fold-on-disconnect never leaves a stale pre-removal
// index on turn.
func (r *Rules) AfterPlayerRemoved(state *game.State, removedIndex int) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	n := len(state.Players)
	if n == 0 {
		return
	}
	extra.DealerIndex = adjustSeatIndex(extra.DealerIndex, removedIndex, n)
	extra.SBIndex = adjustSeatIndex(extra.SBIndex, removedIndex, n)
	extra.BBIndex = adjustSeatIndex(extra.BBIndex, removedIndex, n)

	if extra.HandComplete {
		// The player who was due to deal may be the one who just left, so the turn
		// is re-parked rather than left pointing at an empty seat.
		finishHand(state, extra)
		return
	}

	// A hand is only ever handed to a seat while two players still contest it, and
	// one leave drops that by at most one, so the pot always has a claimant here. The
	// seats have shifted, so the search for the next actor starts on the cursor itself.
	if err := resolveAfterChange(state, extra, (state.CurrentTurn-1+n)%n); err != nil {
		// The hook cannot report it; the hand is already unwound and closed.
		slog.Error("poker cannot finish the hand after a leave",
			"hand", extra.HandNumber, "phase", extra.Phase.String(), "error", err)
	}
}

func cannotAct(extra *State, id string) bool {
	return extra.Folded[id] || extra.PlayersAllIn[id] || extra.PlayerChips[id] == 0
}

// adjustSeatIndex maps a seat marker to its new index after the player
// removed leaves. When the marker's own holder leaves, it moves back to the
// previous seat rather than silently landing on whoever shifted into the slot.
func adjustSeatIndex(seat, removed, nAfter int) int {
	if nAfter <= 0 {
		return 0
	}
	switch {
	case seat > removed:
		seat--
	case seat == removed:
		seat = (removed - 1 + nAfter) % nAfter
	}
	return min(max(seat, 0), nAfter-1)
}

type ActionFold struct{}

func (a ActionFold) Name() string { return "poker.Fold" }

type ActionCheck struct{}

func (a ActionCheck) Name() string { return "poker.Check" }

type ActionCall struct{}

func (a ActionCall) Name() string { return "poker.Call" }

type ActionRaiseTo struct {
	Amount uint
}

func (a ActionRaiseTo) Name() string { return "poker.RaiseTo" }

type ActionAllIn struct{}

func (a ActionAllIn) Name() string { return "poker.AllIn" }

// ActionNextHand deals the next hand of the match. Only the incoming dealer, who
// holds the turn while the result screen is up, may submit it.
type ActionNextHand struct{}

func (a ActionNextHand) Name() string { return "poker.NextHand" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		return validateNextHand(extra)
	}
	if extra.HandComplete || extra.Phase == Showdown {
		return errors.New("hand is over")
	}

	p := state.Players[state.CurrentTurn]
	if extra.Folded[p.ID] || extra.PlayersAllIn[p.ID] {
		return errors.New("player cannot act")
	}

	toCall := ToCall(extra, p.ID)

	switch action := action.(type) {
	case ActionFold:
		return nil
	case ActionCheck:
		if toCall > 0 {
			return errors.New("cannot check, must call or raise")
		}
		return nil
	case ActionCall:
		if toCall == 0 {
			return errors.New("nothing to call")
		}
		return nil
	case ActionRaiseTo:
		return validateRaiseTo(state, extra, p, action.Amount)
	case ActionAllIn:
		if extra.PlayerChips[p.ID] == 0 {
			return errors.New("no chips to go all-in")
		}
		// A shove that lands above the current bet is a raise, and a player who is
		// only owed the difference from a sub-minimum all-in has no raise to make.
		if extra.PlayerBets[p.ID]+extra.PlayerChips[p.ID] > extra.CurrentBet {
			return checkBettingReopened(extra, p)
		}
		return nil
	default:
		return errors.New("action not allowed in poker")
	}
}

func validateNextHand(extra *State) error {
	if !extra.HandComplete {
		return errors.New("the hand is still being played")
	}
	if extra.MatchComplete {
		return errors.New("the match is over")
	}
	return nil
}

// checkBettingReopened refuses a raise from a player who has already acted this
// round unless the bet has since risen by at least a full MinRaise over the level
// they acted on. A full raise clears ActedThisRound outright (see applyBetIncrease);
// the level is what catches several short all-ins that only reach a full raise
// together, which reopen the betting just the same (the TDA rule). Anyone else still
// on turn with ActedThisRound set is facing less than a full raise: they owe the
// difference and may only call or fold.
func checkBettingReopened(extra *State, p *game.Player) error {
	if extra.ActedThisRound[p.ID] && extra.CurrentBet-extra.LastBetLevel[p.ID] < extra.MinRaise {
		return errors.New("betting is not reopened, you may only call or fold")
	}
	return nil
}

func validateRaiseTo(state *game.State, extra *State, p *game.Player, amount uint) error {
	if err := checkBettingReopened(extra, p); err != nil {
		return err
	}
	if amount <= extra.CurrentBet {
		return errors.New("raise must be above current bet")
	}
	if callable := largestCallableBet(state, extra, p); amount > callable {
		return fmt.Errorf("no opponent can call more than %d", callable)
	}
	additional := amount - extra.PlayerBets[p.ID]
	if additional > extra.PlayerChips[p.ID] {
		return errors.New("not enough chips")
	}
	if lo, _ := raiseRange(state, extra, p); amount < lo {
		return fmt.Errorf("minimum raise is %d", lo-extra.CurrentBet)
	}
	return nil
}

// raiseRange is the band of legal raise-to amounts before the reopen and
// above-the-bet checks: a full raise at the bottom, the smaller of the player's own
// stack and what any opponent can call at the top. When the top is below a full
// raise the only raise left is the top itself - the player's own all-in, or putting
// a short opponent all-in, which is chips that opponent can actually call.
func raiseRange(state *game.State, extra *State, p *game.Player) (lo, hi uint) {
	hi = min(extra.PlayerBets[p.ID]+extra.PlayerChips[p.ID], largestCallableBet(state, extra, p))
	return min(extra.CurrentBet+extra.MinRaise, hi), hi
}

// RaiseBounds is the range of ActionRaiseTo amounts ValidateAction accepts from
// playerID right now, whose turn it is aside. ok is false when that player has no
// raise to make at all. Views read it rather than re-deriving the band, so the
// prompt can only ever offer an amount the rules take.
func RaiseBounds(state *game.State, playerID string) (lo, hi uint, ok bool) {
	extra, isPoker := state.Extra.(*State)
	if !isPoker || extra.HandComplete {
		return 0, 0, false
	}
	i := slices.IndexFunc(state.Players, func(p *game.Player) bool { return p.ID == playerID })
	if i < 0 {
		return 0, 0, false
	}
	p := state.Players[i]
	if cannotAct(extra, p.ID) || checkBettingReopened(extra, p) != nil {
		return 0, 0, false
	}
	lo, hi = raiseRange(state, extra, p)
	if hi <= extra.CurrentBet {
		return 0, 0, false
	}
	return lo, hi, true
}

// largestCallableBet is the highest street total any opponent still in the hand could
// match: their own effective stack, what they already have out plus what is behind it.
// Betting past it is chips nobody can call, and the showdown hands them straight back,
// so the raise is refused rather than staged - a slider that stops at the effective
// stack is what every client does with the same situation.
func largestCallableBet(state *game.State, extra *State, p *game.Player) uint {
	var best uint
	for _, o := range contenders(state, extra) {
		if o.ID == p.ID {
			continue
		}
		best = max(best, extra.PlayerBets[o.ID]+extra.PlayerChips[o.ID])
	}
	return best
}

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		// Dealing happens in AfterAction, the only hook that can report a bad deal.
		return nil
	}
	p := state.Players[state.CurrentTurn]

	switch action := action.(type) {
	case ActionFold:
		extra.Folded[p.ID] = true
	case ActionCall:
		commitTo(extra, p, extra.CurrentBet)
	case ActionRaiseTo:
		commitTo(extra, p, action.Amount)
		applyBetIncrease(extra, state, p, extra.PlayerBets[p.ID])
	case ActionAllIn:
		newBet := extra.PlayerBets[p.ID] + extra.PlayerChips[p.ID]
		wasRaise := newBet > extra.CurrentBet
		commitTo(extra, p, newBet)
		if wasRaise {
			applyBetIncrease(extra, state, p, extra.PlayerBets[p.ID])
		}
	}
	extra.ActedThisRound[p.ID] = true
	extra.LastBetLevel[p.ID] = extra.CurrentBet
	return nil
}

// commitTo raises the player's street bet to streetTotal, clamped to the chips they
// actually have - so it doubles as "call what is owed" for a stack too short to cover
// it.
func commitTo(extra *State, p *game.Player, streetTotal uint) {
	if streetTotal < extra.PlayerBets[p.ID] {
		return
	}
	additional := min(streetTotal-extra.PlayerBets[p.ID], extra.PlayerChips[p.ID])
	extra.PlayerChips[p.ID] -= additional
	extra.PlayerBets[p.ID] += additional
	extra.TotalContributed[p.ID] += additional
	extra.MainPool += additional
	if extra.PlayerChips[p.ID] == 0 {
		extra.PlayersAllIn[p.ID] = true
	}
}

// applyBetIncrease raises CurrentBet to newBet. Only a full-size raise
// (>= MinRaise) reopens the round for everyone; a sub-minimum all-in advances the
// amount owed without granting already-acted players fresh action, unless it and
// the short all-ins before it add up to a full raise over what that player last
// acted on (checkBettingReopened).
//
// Deviation worth naming: MinRaise becomes the size of the last full raise, so after
// a sub-minimum all-in the next legal raise is measured from the raised CurrentBet.
// That is one level above the standard rule, which keeps the minimum at the last
// *complete* raise increment. It only ever asks for more, never less.
func applyBetIncrease(extra *State, state *game.State, raiser *game.Player, newBet uint) {
	if newBet <= extra.CurrentBet {
		return
	}
	raiseSize := newBet - extra.CurrentBet
	full := raiseSize >= extra.MinRaise
	extra.CurrentBet = newBet
	if full {
		extra.MinRaise = raiseSize
		resetActedExcept(extra, state, raiser.ID)
	}
}

func resetActedExcept(extra *State, state *game.State, exceptID string) {
	for _, p := range state.Players {
		if p.ID == exceptID || extra.Folded[p.ID] || extra.PlayersAllIn[p.ID] {
			continue
		}
		extra.ActedThisRound[p.ID] = false
	}
}

func (r *Rules) AfterAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		return r.beginHandOrFinish(state, extra, nextFundedSeat(state, extra, extra.DealerIndex))
	}
	return r.afterBettingAction(state, extra)
}

func (r *Rules) afterBettingAction(state *game.State, extra *State) error {
	if extra.HandComplete {
		return nil
	}
	// Only a live player is ever given the turn, and a fold takes one live player out
	// of a field of at least two, so the pot always still has a claimant.
	return resolveAfterChange(state, extra, state.CurrentTurn)
}

// resolveAfterChange moves the hand on after anything that can take a player out of
// it, a betting action or a leave: a lone contender takes the pot, an unfinished
// round goes to the next seat after from that owes an action, and a finished one is
// settled onto the next street or the showdown.
//
// A street that cannot be dealt is unwound by settleOrUnwind and the hand closed, so
// the error only reports what already happened; the betting path hands it to the
// engine, which ends the match on it.
func resolveAfterChange(state *game.State, extra *State, from int) error {
	live := contenders(state, extra)
	if len(live) == 1 {
		awardUncontested(extra, live[0])
		extra.Winners = live
		finishHand(state, extra)
		return nil
	}

	if !bettingRoundComplete(state, extra) {
		if next := nextToAct(state, extra, from); next >= 0 {
			setTurn(state, next)
			return nil
		}
	}

	err := settleOrUnwind(state, extra)
	if err != nil || extra.HandComplete {
		finishHand(state, extra)
		return err
	}
	setTurn(state, firstToActPostflop(state, extra))
	return nil
}

func setTurn(state *game.State, idx int) {
	state.CurrentTurn = idx
	state.OverrideNextTurn = &idx
}

// ToCall returns chips the player must add to match CurrentBet.
func ToCall(extra *State, playerID string) uint {
	if extra.CurrentBet <= extra.PlayerBets[playerID] {
		return 0
	}
	return extra.CurrentBet - extra.PlayerBets[playerID]
}

func activePlayers(state *game.State, extra *State) []*game.Player {
	out := make([]*game.Player, 0, len(state.Players))
	for _, p := range state.Players {
		if !extra.Folded[p.ID] {
			out = append(out, p)
		}
	}
	return out
}
