package ginrummy

import (
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

// applyLayoffs lays opponent deadwood off onto knockerMelds, repeating until no card
// attaches: an earlier layoff can open a new end. The grown melds are scaffolding for
// that loop and nothing reads them afterwards, so only the two answers a score needs
// come back. laidOff is what moved, in the order it was consumed: reconstructing it by
// diffing the two hands is guesswork about something this loop already knew.
//
// knockerMelds is cloned: the caller's copy is the knocker's scored arrangement and
// appending to it in place would grow the melds the hand is settled on.
func applyLayoffs(
	opponentDeadwood []deck.Card, knockerMelds [][]deck.Card,
) (remaining, laidOff []deck.Card) {
	melds := cloneMelds(knockerMelds)
	remaining = slices.Clone(opponentDeadwood)

	for changed := true; changed; {
		changed = false
		for i := 0; i < len(remaining); {
			idx, ok := findAttach(remaining[i], melds)
			if !ok {
				i++
				continue
			}
			melds[idx] = append(melds[idx], remaining[i])
			laidOff = append(laidOff, remaining[i])
			remaining = slices.Delete(remaining, i, i+1)
			changed = true
		}
	}
	return remaining, laidOff
}

// findAttach picks the meld a card lays off onto, runs before sets. A run has two
// open ends and every attachment opens another, while a set stops dead at four:
// spending a card on the set when it also fits a run can strand the deadwood that
// would have extended the run behind it.
func findAttach(card deck.Card, melds [][]deck.Card) (int, bool) {
	if i := slices.IndexFunc(melds, func(m []deck.Card) bool { return isRun(m) && extendsRun(card, m) }); i >= 0 {
		return i, true
	}
	if i := slices.IndexFunc(melds, func(m []deck.Card) bool { return isSet(m) && extendsSet(card, m) }); i >= 0 {
		return i, true
	}
	return 0, false
}

// extendsSet is a card of the set's rank while the set still has a suit free.
func extendsSet(card deck.Card, set []deck.Card) bool {
	return len(set) < maxSetSize && card.Rank == set[0].Rank
}

// extendsRun is a card of the run's suit that sits on either end of it.
func extendsRun(card deck.Card, run []deck.Card) bool {
	lo, hi := slices.MinFunc(run, byRunOrder), slices.MaxFunc(run, byRunOrder)
	if card.Suit != lo.Suit {
		return false
	}
	v := deck.RunOrder(card.Rank)
	return v == deck.RunOrder(lo.Rank)-1 || v == deck.RunOrder(hi.Rank)+1
}
