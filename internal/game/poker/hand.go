package poker

import (
	"errors"
	"log/slog"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// holeCards is what each funded seat is dealt at the start of a hand.
const holeCards = 2

// minDeckAfterDeal is burn+flop+turn+river plus a two-card margin, checked once
// the hole cards for the hand are out.
const minDeckAfterDeal = 1 + 3 + 1 + 1 + 2

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
