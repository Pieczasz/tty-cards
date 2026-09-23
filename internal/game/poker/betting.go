package poker

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/game"
)

func cannotAct(extra *State, id string) bool {
	return extra.Folded[id] || extra.PlayersAllIn[id] || extra.PlayerChips[id] == 0
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
// MinRaise is the size of the last full raise, and the next legal raise is measured
// from the raised CurrentBet: a sub-minimum all-in moves the bet but not the increment,
// which is the standard rule (decisions.md #41).
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
