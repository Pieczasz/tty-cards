package poker

import (
	"cmp"
	"errors"
	"log/slog"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/game"
)

// owesAction is a seat that can act and has not yet acted on, or not yet matched, the
// current bet.
func (s *State) owesAction(seat *Seat) bool {
	return seat.canAct() && (!seat.Acted || seat.Bet < s.CurrentBet)
}

func bettingRoundComplete(state *game.State, extra *State) bool {
	return !slices.ContainsFunc(state.Players, func(p *game.Player) bool {
		return extra.owesAction(extra.Seats[p.ID])
	})
}

func nextToAct(state *game.State, extra *State, from int) int {
	return game.NextSeat(from, len(state.Players), func(seat int) bool {
		return extra.owesAction(extra.Seats[state.Players[seat].ID])
	})
}

func firstToActPostflop(state *game.State, extra *State) int {
	seat := game.NextSeat(extra.DealerIndex, len(state.Players), func(seat int) bool {
		return extra.Seats[state.Players[seat].ID].canAct()
	})
	if seat < 0 {
		return state.CurrentTurn
	}
	return seat
}

func settleAndAdvance(state *game.State, extra *State) error {
	for _, p := range state.Players {
		seat := extra.Seats[p.ID]
		seat.Bet = 0
		seat.Acted = false
	}
	for _, seat := range extra.Seats {
		seat.LastBetLevel = 0
	}
	extra.CurrentBet = 0
	extra.MinRaise = extra.BigBlind

	// Betting can only continue with at least two players who still have chips;
	// a lone live player against all-ins just runs the board out.
	canStillBet := 0
	for _, p := range unfolded(state, extra) {
		if seat := extra.Seats[p.ID]; !seat.AllIn && seat.Chips > 0 {
			canStillBet++
		}
	}

	dealt, err := advanceStreet(state, extra)
	if err != nil {
		return err
	}
	if !dealt {
		return runShowdown(state, extra)
	}
	if canStillBet < 2 {
		return runOutBoard(state, extra)
	}
	return nil
}

// settleOrUnwind is settleAndAdvance for callers that cannot finish the hand on a
// failure: a street that cannot be dealt leaves chips no showdown will ever award, so
// the pool goes back to whoever put it in before the error is passed on.
func settleOrUnwind(state *game.State, extra *State) error {
	if err := settleAndAdvance(state, extra); err != nil {
		refundContributions(extra)
		return err
	}
	return nil
}

// advanceStreet burns, deals what the next street needs and moves Phase onto it. It
// reports false once there is no street left to deal, which is the showdown.
func advanceStreet(state *game.State, extra *State) (bool, error) {
	var next Phase
	var cards int
	switch extra.Phase {
	case PhasePreFlop:
		next, cards = PhaseFlop, flopCards
	case PhaseFlop:
		next, cards = PhaseTurn, 1
	case PhaseTurn:
		next, cards = PhaseRiver, 1
	case PhaseUnknown, PhaseRiver, PhaseShowdown:
		return false, nil
	}
	if err := dealCommunity(state, extra, cards); err != nil {
		return false, err
	}
	extra.Phase = next
	return true, nil
}

func dealCommunity(state *game.State, extra *State, n int) error {
	if _, ok := state.Deck.Draw(); !ok {
		slog.Error("poker deck empty during burn", "phase", extra.Phase.String(), "hand", extra.HandNumber)
		return errors.New("deck empty during burn")
	}
	for range n {
		c, ok := state.Deck.Draw()
		if !ok {
			slog.Error("poker deck empty during community deal",
				"phase", extra.Phase.String(), "hand", extra.HandNumber, "want", n)
			return errors.New("deck empty during community deal")
		}
		extra.Table = append(extra.Table, c)
	}
	return nil
}

func runOutBoard(state *game.State, extra *State) error {
	for extra.Phase != PhaseRiver && extra.Phase != PhaseShowdown {
		dealt, err := advanceStreet(state, extra)
		if err != nil {
			return err
		}
		if !dealt {
			break
		}
	}
	return runShowdown(state, extra)
}

func runShowdown(state *game.State, extra *State) error {
	extra.Phase = PhaseShowdown
	extra.ReachedShowdown = true
	live := contenders(state, extra)
	scores := handScores(extra, live)
	refundUncalled(extra)
	extra.Pots = buildSidePots(extra, live)
	extra.Winners = awardPots(extra, live, scores)
	return nil
}

// contenders is everyone still contesting the pot: the seated players who have not
// folded, plus anyone who left the table while all-in. An all-in player has no
// decisions left to make, so disconnecting cannot cost them a pot they are already
// committed to - leaving still forfeits the hand for anyone with chips behind.
func contenders(state *game.State, extra *State) []*game.Player {
	out := unfolded(state, extra)
	for _, p := range state.LeftPlayers {
		if seat := extra.Seats[p.ID]; !seat.Folded && seat.AllIn {
			out = append(out, p)
		}
	}
	return out
}

func buildSidePots(extra *State, live []*game.Player) []Pot {
	eligibleIDs := make(map[string]bool, len(live))
	for _, p := range live {
		eligibleIDs[p.ID] = true
	}

	// Distinct non-zero contribution levels, ascending: each one closes a pot
	// layer. Contributions come from every player who put chips in, seated or not.
	levels := make([]uint, 0, len(extra.Seats))
	for _, seat := range extra.Seats {
		levels = append(levels, seat.Contributed)
	}
	slices.Sort(levels)
	levels = slices.Compact(levels)
	levels = slices.DeleteFunc(levels, func(c uint) bool { return c == 0 })

	var pots []Pot
	var orphan uint
	prev := uint(0)
	for _, lvl := range levels {
		var eligible []string
		var amount uint
		for id, seat := range extra.Seats {
			// A contribution below this level is always below prev too: every
			// non-zero contribution is itself one of the levels, so by the time the
			// loop passes it, prev has already reached it. Nothing to collect.
			if seat.Contributed < lvl {
				continue
			}
			amount += lvl - prev
			if eligibleIDs[id] {
				eligible = append(eligible, id)
			}
		}
		prev = lvl
		// A level always has at least one contributor sitting exactly on it - every
		// level is somebody's contribution - and lvl > prev, so amount is never zero.
		amount += orphan
		orphan = 0
		if len(eligible) == 0 {
			// Dead money carries forward to the next pot layer.
			orphan = amount
			continue
		}
		slices.Sort(eligible) // stable pot eligibility order
		pots = append(pots, Pot{Amount: amount, Eligible: eligible})
	}
	if orphan > 0 {
		// Levels above the largest eligible contribution can only hold money somebody
		// matched - refundUncalled already took the unmatched part out - so this is
		// dead money from players who folded, and it rides with the last live layer.
		// A layer always formed: every contender had to match the big blind to be one.
		pots[len(pots)-1].Amount += orphan
	}
	return pots
}

// refundUncalled hands back the slice of the biggest bet that nobody matched. Only the
// single largest contributor can have one - everything at or below the second largest
// contribution was matched by somebody - and it has to leave the pot before the side
// pots are cut: a layer above every eligible player is unwinnable, and folding it into
// the live pot would pay one player's uncalled chips to their opponents.
func refundUncalled(extra *State) {
	var topSeat *Seat
	var top, second uint
	for _, seat := range extra.Seats {
		switch {
		case seat.Contributed > top:
			topSeat, top, second = seat, seat.Contributed, top
		case seat.Contributed > second:
			second = seat.Contributed
		}
	}
	uncalled := top - second
	if uncalled == 0 {
		return
	}
	topSeat.Contributed = second
	topSeat.Chips += uncalled
	extra.Pool -= uncalled
}

// splitEvenly hands amount to ids, the odd chips going one each to the front of the
// slice. Callers sort ids first, which is a deviation worth naming: a casino gives the
// odd chip to the first player left of the button, this gives it to the lowest-sorted
// player ID. It is deterministic, which is what matters for a replayable table.
func splitEvenly(extra *State, ids []string, amount uint) {
	share := amount / uint(len(ids))
	rem := amount % uint(len(ids))
	for i, id := range ids {
		extra.Seats[id].Chips += share
		if uint(i) < rem {
			extra.Seats[id].Chips++
		}
	}
}

// awardPots pays every pot to the best hand among that pot's own eligible players and
// returns everyone who took a share, main pot first.
//
// Eligibility is what makes this the authoritative winner list: the best hand at the
// table can belong to a short stack who only paid into the main pot, so a global
// best-hand scan would announce a winner the side pot did not go to.
//
// Every pot is cut from Pool, so paying one takes it back out rather than the pool
// being zeroed on trust: a layer that never reaches a stack is then still sitting in
// Pool for the conservation check in finishHand to find.
func awardPots(extra *State, live []*game.Player, scores map[string]int) []*game.Player {
	playerByID := make(map[string]*game.Player, len(live))
	for _, p := range live {
		playerByID[p.ID] = p
	}

	var winners []*game.Player
	for _, pot := range extra.Pots {
		// Eligible is never empty and every ID in it is one of live: buildSidePots
		// drops a layer nobody can win, and draws both from the same set.
		bestScore := -1
		var potWinners []string
		for _, id := range pot.Eligible {
			switch score := scores[id]; {
			case score > bestScore:
				bestScore = score
				potWinners = append(potWinners[:0], id)
			case score == bestScore:
				potWinners = append(potWinners, id)
			}
		}
		slices.Sort(potWinners)
		splitEvenly(extra, potWinners, pot.Amount)
		extra.Pool -= pot.Amount
		for _, id := range potWinners {
			if p := playerByID[id]; !slices.Contains(winners, p) {
				winners = append(winners, p)
			}
		}
	}
	return winners
}

// handScores evaluates each player's hand once, so callers avoid re-running the
// allocating evaluator inside a sort comparator or per-pot loop.
func handScores(extra *State, players []*game.Player) map[string]int {
	scores := make(map[string]int, len(players))
	for _, p := range players {
		scores[p.ID] = evaluateHand(slices.Concat(p.Cards, extra.Table))
	}
	return scores
}

// awardUncontested pays the last live player when everyone else folded or left. The
// pot is split the way the showdown path splits it: refundUncalled hands back the one
// slice nobody matched - the top contributor's excess over the second-highest - and
// everything else, dead money from folders included, goes to the winner, just as
// buildSidePots rides it with the last live layer. Refunding each folder their excess
// over the winner instead would let a player who called and folded take back chips
// the showdown path would have paid out.
func awardUncontested(extra *State, winner *game.Player) {
	refundUncalled(extra)
	extra.Seats[winner.ID].Chips += extra.Pool
	extra.Pool = 0
	extra.Pots = nil
}

// refundContributions unwinds the hand, handing every chip in the pool back to whoever
// put it in. It is the only honest exit from a hand that cannot be played out - a deal
// that runs the deck dry leaves chips no showdown will ever award, and finishHand would
// otherwise strand them. Nothing has been paid at that point, so Pool is still
// exactly the sum of the contributions.
func refundContributions(extra *State) {
	for _, seat := range extra.Seats {
		seat.Chips += seat.Contributed
	}
	extra.Pool = 0
	extra.Pots = nil
}

// chipsInPlay is the invariant every betting path must preserve: chips only ever move
// between a player's stack and the pool, so the two together are constant for the
// whole hand.
func chipsInPlay(extra *State) uint {
	total := extra.Pool
	for _, seat := range extra.Seats {
		total += seat.Chips
	}
	return total
}

// rankPlayers ranks everyone who sat down, with the players who walked out last.
// Leaving mid-match forfeits the match, so no leaver places above someone who saw
// it through - but leavers are still ranked against each other on what they won
// while they were playing, not on who happened to quit first.
func rankPlayers(state *game.State, extra *State) []*game.Player {
	byResult := resultOrder(state, extra)

	seated := slices.Clone(state.Players)
	left := slices.Clone(state.LeftPlayers)
	slices.SortFunc(seated, byResult)
	slices.SortFunc(left, byResult)
	return slices.Concat(seated, left)
}

// resultOrder is resultLevel with the ID as a final tiebreak, so Standings is a total
// order and a chop renders in a stable sequence.
func resultOrder(state *game.State, extra *State) func(a, b *game.Player) int {
	level := resultLevel(state, extra)
	return func(a, b *game.Player) int {
		if c := level(a, b); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	}
}

// resultLevel compares two players by what they actually did: chips desc, bust-out
// hand desc, active before folded, hand score desc. Chips lead because a match is
// decided by the stack a player walks away with; everyone who busted is level on
// chips, so how long they lasted is what separates them. The hand-level keys only
// matter for players who finished holding equal stacks. Zero is a genuine draw.
//
// Hand score counts only for a hand that was shown down between two players still
// seated: a pot won face-down was never contested on the cards, and a leaver's hand
// was never played out, so ranking on either splits a draw by cards nobody showed.
func resultLevel(state *game.State, extra *State) func(a, b *game.Player) int {
	scores := handScores(extra, slices.Concat(state.Players, state.LeftPlayers))
	seated := make(map[string]bool, len(state.Players))
	for _, p := range state.Players {
		seated[p.ID] = true
	}
	return func(a, b *game.Player) int {
		sa, sb := extra.Seats[a.ID], extra.Seats[b.ID]
		if c := cmp.Or(
			cmp.Compare(sb.Chips, sa.Chips),
			cmp.Compare(sb.BustedAtHand, sa.BustedAtHand),
			compareFolded(sa.Folded, sb.Folded),
		); c != 0 || sa.Folded {
			return c
		}
		if extra.ReachedShowdown && seated[a.ID] && seated[b.ID] {
			return cmp.Compare(scores[b.ID], scores[a.ID])
		}
		return 0
	}
}

// compareFolded ranks a player still in the hand ahead of one who folded.
func compareFolded(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}
