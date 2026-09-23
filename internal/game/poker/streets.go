package poker

import (
	"cmp"
	"errors"
	"log/slog"
	"maps"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/game"
)

func bettingRoundComplete(state *game.State, extra *State) bool {
	for _, p := range state.Players {
		if cannotAct(extra, p.ID) {
			continue
		}
		if !extra.ActedThisRound[p.ID] {
			return false
		}
		if extra.PlayerBets[p.ID] < extra.CurrentBet {
			return false
		}
	}
	return true
}

// nextSeat scans clockwise from seat from, wrapping once around a table of n seats,
// and returns the first index ok accepts. -1 means no seat qualifies.
func nextSeat(from, n int, ok func(idx int) bool) int {
	for i := 1; i <= n; i++ {
		idx := (from + i) % n
		if ok(idx) {
			return idx
		}
	}
	return -1
}

func nextToAct(state *game.State, extra *State, from int) int {
	return nextSeat(from, len(state.Players), func(idx int) bool {
		id := state.Players[idx].ID
		if cannotAct(extra, id) {
			return false
		}
		return !extra.ActedThisRound[id] || extra.PlayerBets[id] < extra.CurrentBet
	})
}

func firstToActPostflop(state *game.State, extra *State) int {
	idx := nextSeat(extra.DealerIndex, len(state.Players), func(idx int) bool {
		return !cannotAct(extra, state.Players[idx].ID)
	})
	if idx < 0 {
		return state.CurrentTurn
	}
	return idx
}

func settleAndAdvance(state *game.State, extra *State) error {
	for _, p := range state.Players {
		extra.PlayerBets[p.ID] = 0
		extra.ActedThisRound[p.ID] = false
	}
	clear(extra.LastBetLevel)
	extra.CurrentBet = 0
	extra.MinRaise = extra.BigBlind

	// Betting can only continue with at least two players who still have chips;
	// a lone live player against all-ins just runs the board out.
	canStillBet := 0
	for _, p := range activePlayers(state, extra) {
		if !extra.PlayersAllIn[p.ID] && extra.PlayerChips[p.ID] > 0 {
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
	var next RoundPhase
	var cards int
	switch extra.Phase {
	case PreFlop:
		next, cards = Flop, 3
	case Flop:
		next, cards = Turn, 1
	case Turn:
		next, cards = River, 1
	default:
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
	for extra.Phase != River && extra.Phase != Showdown {
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
	extra.Phase = Showdown
	extra.ReachedShowdown = true
	live := contenders(state, extra)
	scores := handScores(live, extra)
	refundUncalled(extra)
	extra.Pots = buildSidePots(extra, live)
	extra.HandComplete = true
	extra.Winners = awardPots(extra, live, scores)
	return nil
}

// contenders is everyone still contesting the pot: the seated players who have not
// folded, plus anyone who left the table while all-in. An all-in player has no
// decisions left to make, so disconnecting cannot cost them a pot they are already
// committed to - leaving still forfeits the hand for anyone with chips behind.
func contenders(state *game.State, extra *State) []*game.Player {
	out := activePlayers(state, extra)
	for _, p := range state.LeftPlayers {
		if !extra.Folded[p.ID] && extra.PlayersAllIn[p.ID] {
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
	levels := slices.Sorted(maps.Values(extra.TotalContributed))
	levels = slices.Compact(levels)
	levels = slices.DeleteFunc(levels, func(c uint) bool { return c == 0 })

	var pots []Pot
	var orphan uint
	prev := uint(0)
	for _, lvl := range levels {
		var eligible []string
		var amount uint
		for id, contrib := range extra.TotalContributed {
			// A contribution below this level is always below prev too: every
			// non-zero contribution is itself one of the levels, so by the time the
			// loop passes it, prev has already reached it. Nothing to collect.
			if contrib < lvl {
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
	var topID string
	var top, second uint
	for id, contributed := range extra.TotalContributed {
		switch {
		case contributed > top:
			topID, top, second = id, contributed, top
		case contributed > second:
			second = contributed
		}
	}
	uncalled := top - second
	if uncalled == 0 {
		return
	}
	extra.TotalContributed[topID] = second
	extra.PlayerChips[topID] += uncalled
	extra.MainPool -= uncalled
}

// splitEvenly hands amount to ids, the odd chips going one each to the front of the
// slice. Callers sort ids first, which is a deviation worth naming: a casino gives the
// odd chip to the first player left of the button, this gives it to the lowest-sorted
// player ID. It is deterministic, which is what matters for a replayable table.
func splitEvenly(extra *State, ids []string, amount uint) {
	share := amount / uint(len(ids))
	rem := amount % uint(len(ids))
	for i, id := range ids {
		extra.PlayerChips[id] += share
		if uint(i) < rem {
			extra.PlayerChips[id]++
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
// Every pot is cut from MainPool, so paying one takes it back out rather than the pool
// being zeroed on trust: a layer that never reaches a stack is then still sitting in
// MainPool for the conservation check in finishHand to find.
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
		extra.MainPool -= pot.Amount
		for _, id := range potWinners {
			if p := playerByID[id]; !slices.Contains(winners, p) {
				winners = append(winners, p)
			}
		}
	}
	return winners
}

func handScore(p *game.Player, extra *State) int {
	cards := slices.Clone(p.Cards)
	cards = append(cards, extra.Table...)
	return evaluateHand(cards)
}

// handScores evaluates each player's hand once, so callers avoid re-running the
// allocating evaluator inside a sort comparator or per-pot loop.
func handScores(players []*game.Player, extra *State) map[string]int {
	scores := make(map[string]int, len(players))
	for _, p := range players {
		scores[p.ID] = handScore(p, extra)
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
	extra.PlayerChips[winner.ID] += extra.MainPool
	extra.MainPool = 0
	extra.Pots = nil
}

// refundContributions unwinds the hand, handing every chip in the pool back to whoever
// put it in. It is the only honest exit from a hand that cannot be played out - a deal
// that runs the deck dry leaves chips no showdown will ever award, and finishHand would
// otherwise strand them. Nothing has been paid at that point, so MainPool is still
// exactly the sum of the contributions.
func refundContributions(extra *State) {
	for id, contributed := range extra.TotalContributed {
		extra.PlayerChips[id] += contributed
	}
	extra.MainPool = 0
	extra.Pots = nil
}

// chipsInPlay is the invariant every betting path must preserve: chips only ever move
// between a player's stack and the pool, so the two together are constant for the
// whole hand.
func chipsInPlay(extra *State) uint {
	total := extra.MainPool
	for _, c := range extra.PlayerChips {
		total += c
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
	scores := handScores(slices.Concat(state.Players, state.LeftPlayers), extra)
	seated := make(map[string]bool, len(state.Players))
	for _, p := range state.Players {
		seated[p.ID] = true
	}
	return func(a, b *game.Player) int {
		if c := cmp.Compare(extra.PlayerChips[b.ID], extra.PlayerChips[a.ID]); c != 0 {
			return c
		}
		if c := cmp.Compare(extra.BustedAtHand[b.ID], extra.BustedAtHand[a.ID]); c != 0 {
			return c
		}
		fa, fb := extra.Folded[a.ID], extra.Folded[b.ID]
		if fa != fb {
			if fa {
				return 1
			}
			return -1
		}
		if !fa && extra.ReachedShowdown && seated[a.ID] && seated[b.ID] {
			return cmp.Compare(scores[b.ID], scores[a.ID])
		}
		return 0
	}
}
