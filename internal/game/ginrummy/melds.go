package ginrummy

import (
	"cmp"
	"math"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

func deadwoodPoints(c deck.Card) int { return deck.PipValue(c.Rank) }

func sumDeadwood(cards []deck.Card) int {
	total := 0
	for _, c := range cards {
		total += deadwoodPoints(c)
	}
	return total
}

// maskBits is how many cards the meld search can address: candidate melds are
// uint16 index masks.
const maskBits = 16

// pick appends to dst the cards whose bit in mask is in, or whose bit is clear when in
// is false: a meld is the cards in its mask, the deadwood the cards outside them all.
func pick(dst, cards []deck.Card, mask uint16, in bool) []deck.Card {
	for i, c := range cards {
		if (mask&(1<<i) != 0) == in {
			dst = append(dst, c)
		}
	}
	return dst
}

// maskOf is the mask with a bit set for every index in idxs.
func maskOf(idxs []int) uint16 {
	var mask uint16
	for _, i := range idxs {
		mask |= 1 << i
	}
	return mask
}

// byRunOrder sorts cards the way a run reads, ace low.
func byRunOrder(a, b deck.Card) int {
	return cmp.Compare(deck.RunOrder(a.Rank), deck.RunOrder(b.Rank))
}

// bestMeldSplit partitions hand into melds minimizing deadwood points.
func bestMeldSplit(hand []deck.Card) (melds [][]deck.Card, deadwood []deck.Card, deadwoodPts int) {
	return bestSplitBy(hand, sumDeadwood)
}

// bestMeldSplitAgainst is the arrangement a defender is entitled to when the knocker
// has knocked: the one that minimizes deadwood *after* laying off. Minimizing raw
// deadwood first can strand cards that would have attached to a knocker meld, which
// overcharges the defender and can cost them an undercut they had earned.
//
// deadwoodPts is the post-layoff total; deadwood is still the pre-layoff set, so the
// caller runs applyLayoffs on it to learn which cards actually moved.
func bestMeldSplitAgainst(
	hand []deck.Card, knockerMelds [][]deck.Card,
) (melds [][]deck.Card, deadwood []deck.Card, deadwoodPts int) {
	return bestSplitBy(hand, func(dw []deck.Card) int {
		remaining, _ := applyLayoffs(dw, knockerMelds)
		return sumDeadwood(remaining)
	})
}

// bestSplitBy searches every disjoint set of candidate melds and keeps the split
// score rates lowest. score is handed the deadwood the split leaves.
func bestSplitBy(
	hand []deck.Card, score func(deadwood []deck.Card) int,
) (melds [][]deck.Card, deadwood []deck.Card, deadwoodPts int) {
	n := len(hand)
	if n > maskBits {
		// Candidate melds are uint16 index masks. A bigger hand is a caller mistake
		// (deal is 10, hold is 11). The engine now recovers a panic on the timer
		// goroutine, but that ends the table; treating the hand as deadwood keeps it
		// playing instead of scoring a silently truncated search.
		deadwood = slices.Clone(hand)
		return nil, deadwood, score(deadwood)
	}
	cards := slices.Clone(hand)
	candidates := generateMeldMasks(cards)

	bestPts := math.MaxInt
	var bestMasks []uint16
	buf := make([]deck.Card, 0, n)

	var search func(start int, used uint16, chosen []uint16)
	search = func(start int, used uint16, chosen []uint16) {
		// Reused across the whole search: score reads it before we recurse, and
		// applyLayoffs clones what it keeps.
		buf = pick(buf[:0], cards, used, false)
		pts := score(buf)
		if pts < bestPts {
			bestPts = pts
			bestMasks = slices.Clone(chosen)
		}
		if pts == 0 {
			return
		}
		for i := start; i < len(candidates); i++ {
			m := candidates[i]
			if used&m != 0 {
				continue
			}
			search(i+1, used|m, append(chosen, m))
		}
	}
	search(0, 0, nil)

	melds = make([][]deck.Card, 0, len(bestMasks))
	var used uint16
	for _, mask := range bestMasks {
		melds = append(melds, pick(make([]deck.Card, 0, maxSetSize), cards, mask, true))
		used |= mask
	}
	return melds, pick(make([]deck.Card, 0, n), cards, used, false), bestPts
}

func generateMeldMasks(cards []deck.Card) []uint16 {
	masks := append(setMasks(cards), runMasks(cards)...)
	// setMasks and runMasks walk maps, and bestSplitBy keeps the first split it
	// finds at the best score. Without an order the melds a knock is scored on
	// change between runs on the same hand.
	slices.Sort(masks)
	return masks
}

func setMasks(cards []deck.Card) []uint16 {
	byRank := map[deck.Rank][]int{}
	for i, c := range cards {
		byRank[c.Rank] = append(byRank[c.Rank], i)
	}
	var out []uint16
	for _, idxs := range byRank {
		for size := minMeldSize; size <= min(len(idxs), maxSetSize); size++ {
			for _, combo := range combinations(idxs, size) {
				out = append(out, maskOf(combo))
			}
		}
	}
	return out
}

func runMasks(cards []deck.Card) []uint16 {
	bySuit := map[deck.Suit][]int{}
	for i, c := range cards {
		bySuit[c.Suit] = append(bySuit[c.Suit], i)
	}
	out := make([]uint16, 0, len(cards))
	for _, idxs := range bySuit {
		slices.SortFunc(idxs, func(a, b int) int { return byRunOrder(cards[a], cards[b]) })
		out = append(out, runMasksInSuit(cards, idxs)...)
	}
	return out
}

func runMasksInSuit(cards []deck.Card, idxs []int) []uint16 {
	var out []uint16
	start := 0
	for start < len(idxs) {
		end := start + 1
		for end < len(idxs) &&
			deck.RunOrder(cards[idxs[end]].Rank) == deck.RunOrder(cards[idxs[end-1]].Rank)+1 {
			end++
		}
		out = append(out, subRunMasks(idxs[start:end])...)
		start = end
	}
	return out
}

func subRunMasks(block []int) []uint16 {
	if len(block) < minMeldSize {
		return nil
	}
	var out []uint16
	for i := range block {
		for j := i + minMeldSize; j <= len(block); j++ {
			out = append(out, maskOf(block[i:j]))
		}
	}
	return out
}

func combinations(items []int, k int) [][]int {
	if k > len(items) || k <= 0 {
		return nil
	}
	var out [][]int
	var walk func(start int, cur []int)
	walk = func(start int, cur []int) {
		if len(cur) == k {
			out = append(out, slices.Clone(cur))
			return
		}
		for i := start; i < len(items); i++ {
			walk(i+1, append(cur, items[i]))
		}
	}
	walk(0, nil)
	return out
}

// highestPointCard is the priciest card to be caught holding. Empty input returns the
// zero Card, which is detectably empty because standard ranks start at 1.
func highestPointCard(cards []deck.Card) deck.Card {
	if len(cards) == 0 {
		return deck.Card{}
	}
	return slices.MaxFunc(cards, func(a, b deck.Card) int {
		return cmp.Compare(deadwoodPoints(a), deadwoodPoints(b))
	})
}

func isSet(meld []deck.Card) bool {
	if len(meld) < minMeldSize || len(meld) > maxSetSize {
		return false
	}
	rank := meld[0].Rank
	for _, c := range meld[1:] {
		if c.Rank != rank {
			return false
		}
	}
	return true
}

func isRun(meld []deck.Card) bool {
	if len(meld) < minMeldSize {
		return false
	}
	sorted := slices.SortedFunc(slices.Values(meld), byRunOrder)

	suit := sorted[0].Suit
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Suit != suit {
			return false
		}
		if deck.RunOrder(sorted[i].Rank) != deck.RunOrder(sorted[i-1].Rank)+1 {
			return false
		}
	}
	return true
}
