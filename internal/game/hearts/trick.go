package hearts

import (
	"log/slog"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

func findTwoOfClubs(state *game.State) int {
	for i, p := range state.Players {
		if slices.Contains(p.Cards, twoOfClubs) {
			return i
		}
	}
	return 0
}

func passRecipient(from int, dir PassDirection, n int) int {
	switch dir {
	case PassLeft:
		return (from + 1) % n
	case PassRight:
		return (from - 1 + n) % n
	case PassAcross:
		return (from + 2) % n
	default:
		return from
	}
}

func applyAllPasses(state *game.State, extra *State) {
	n := len(state.Players)
	for i, p := range state.Players {
		recipient := passRecipient(i, extra.PassDirection, n)
		state.Players[recipient].Cards = append(state.Players[recipient].Cards, extra.PendingPasses[p.ID]...)
	}
}

func nextUnpassedSeat(state *game.State, extra *State, from int) int {
	n := len(state.Players)
	for step := 1; step <= n; step++ {
		seat := (from + step) % n
		if !extra.Passed[state.Players[seat].ID] {
			return seat
		}
	}
	return from
}

func handHasSuit(hand []deck.Card, suit deck.Suit) bool {
	return slices.ContainsFunc(hand, func(c deck.Card) bool { return c.Suit == suit })
}

func onlyHearts(hand []deck.Card) bool {
	return len(hand) > 0 &&
		!slices.ContainsFunc(hand, func(c deck.Card) bool { return c.Suit != deck.Hearts })
}

func isPenaltyCard(c deck.Card) bool {
	return c.Suit == deck.Hearts || c == queenOfSpades
}

func hasNonPenaltyCard(hand []deck.Card) bool {
	return slices.ContainsFunc(hand, func(c deck.Card) bool { return !isPenaltyCard(c) })
}

func trickWinner(state *game.State, extra *State) (string, int) {
	bestValue := -1
	winnerSeat := extra.TrickLeader
	for seat, p := range state.Players {
		card, ok := extra.TrickCards[p.ID]
		if !ok || card.Suit != extra.LedSuit {
			continue
		}
		if v := deck.RankValue(card.Rank); v > bestValue {
			bestValue = v
			winnerSeat = seat
		}
	}
	return state.Players[winnerSeat].ID, winnerSeat
}

func trickPoints(cards map[string]deck.Card) int {
	pts := 0
	for _, c := range cards {
		if c.Suit == deck.Hearts {
			pts++
		}
		if c == queenOfSpades {
			pts += 13
		}
	}
	return pts
}

func scoreHand(extra *State, players []*game.Player) {
	shooterID := ""
	for _, p := range players {
		if extra.HandPoints[p.ID] == penaltyPointsTotal {
			shooterID = p.ID
			break
		}
	}
	if shooterID != "" {
		// The moon moves a full hand of points onto three players who took nothing,
		// so it is the one result of a hand that gets disputed. Nothing reads this
		// back; it is the only server-side record that it happened at all.
		slog.Info("hearts moon shot",
			"hand", extra.HandNumber, "shooter", shooterID, "points_each", penaltyPointsTotal)
		for _, p := range players {
			if p.ID != shooterID {
				extra.CumulativeScores[p.ID] += penaltyPointsTotal
			}
		}
	} else {
		for _, p := range players {
			extra.CumulativeScores[p.ID] += extra.HandPoints[p.ID]
		}
	}
	slog.Info("hearts hand scored",
		"hand", extra.HandNumber,
		"pass_direction", extra.PassDirection.String(),
		"hand_points", extra.HandPoints,
		"totals", extra.CumulativeScores)
}

// threeMostDangerous is the absent player's pass: the Q♠, then the A♠ and K♠ that
// catch her, then the highest hearts, then the highest of the rest. Passing the lowest
// cards instead kept every card that takes points.
func threeMostDangerous(hand []deck.Card) []deck.Card {
	if len(hand) <= cardsToPass {
		return slices.Clone(hand)
	}
	sorted := slices.Clone(hand)
	slices.SortFunc(sorted, func(a, b deck.Card) int {
		if d := passDanger(b) - passDanger(a); d != 0 {
			return d
		}
		return int(a.Suit) - int(b.Suit)
	})
	return sorted[:cardsToPass]
}

func passDanger(c deck.Card) int {
	const spadeTier, heartTier = 100, 50
	switch {
	case c == queenOfSpades:
		return spadeTier + 2
	case c.Suit == deck.Spades && (c.Rank == deck.Ace || c.Rank == deck.King):
		return spadeTier + deck.RankValue(c.Rank) - deck.RankValue(deck.King)
	case c.Suit == deck.Hearts:
		return heartTier + deck.RankValue(c.Rank)
	default:
		return deck.RankValue(c.Rank)
	}
}

func firstLegalCard(extra *State, p *game.Player) (deck.Card, bool) {
	for _, c := range p.Cards {
		if validatePlayCard(extra, p, c) == nil {
			return c, true
		}
	}
	return deck.Card{}, false
}
