// Package elo is the pure rating maths: Simple Multiplayer Elo over a finish order,
// with the provisional-account rule applied per pair. It knows nothing about storage.
package elo

import (
	"cmp"
	"log/slog"
	"math"
	"slices"
)

// The rating scale. An unrated player starts at DefaultRating, and Calculate keeps
// every result inside [MinRating, MaxRating].
const (
	DefaultRating float64 = 1500.0
	MinRating     float64 = 100.0
	MaxRating     float64 = 4000.0

	// kFactor determines how much ratings can change in a single match. 32 is the
	// standard chess default; a tiered K (higher for new accounts, lower once
	// established) is the upgrade path if rating volatility becomes a problem.
	kFactor float64 = 32.0
)

// Player is one seat in a Calculate: who, at what rating, finishing where.
type Player struct {
	ID          string
	Rating      float64
	Place       int
	Provisional bool
}

// ToUint32 is a rating as it is stored: clamped to the scale and rounded.
func ToUint32(rating float64) uint32 {
	return uint32(math.Round(clampRating(rating)))
}

// Calculate applies the Simple Multiplayer Elo (SME) algorithm.
// https://www.tckerrigan.com/Misc/Multiplayer_Elo/
// The player slice MUST be sorted by performance, from 1st place (index 0) to last place (index n-1).
func Calculate(players []Player) map[string]float64 {
	n := len(players)
	newRatings := make(map[string]float64, n)

	if n == 0 {
		return newRatings
	}
	if n == 1 {
		newRatings[players[0].ID] = clampRating(players[0].Rating)
		return newRatings
	}

	ordered := slices.Clone(players)
	normalizeTies(ordered)

	deltas := make([]float64, n)
	for i := range n - 1 {
		moved := capTransfer(
			rawTransfer(ordered[i], ordered[i+1]),
			ordered[i].Rating+deltas[i],
			ordered[i+1].Rating+deltas[i+1],
		)
		up, down := unpaidAgainstProvisional(ordered[i], ordered[i+1], moved, -moved)
		deltas[i] += up
		deltas[i+1] += down
	}

	for i, player := range ordered {
		if _, duplicate := newRatings[player.ID]; duplicate {
			slog.Error("duplicate player id in an elo calculation; one rating change is discarded",
				"player_id", player.ID, "players", len(ordered))
		}
		newRatings[player.ID] = clampRating(player.Rating + deltas[i])
	}

	return newRatings
}

func clampRating(rating float64) float64 {
	if math.IsNaN(rating) {
		slog.Error("NaN rating clamped to the default", "default", DefaultRating)
		return DefaultRating
	}
	return min(max(rating, MinRating), MaxRating)
}

func unpaidAgainstProvisional(upper, lower Player, up, down float64) (float64, float64) {
	if upper.Provisional == lower.Provisional {
		return up, down
	}
	if !upper.Provisional {
		up = min(up, 0)
	}
	if !lower.Provisional {
		down = min(down, 0)
	}
	return up, down
}

func normalizeTies(players []Player) {
	for start := 0; start < len(players); {
		end := start + 1
		for end < len(players) && players[end].Place != 0 && players[end].Place == players[start].Place {
			end++
		}
		if end-start > 1 {
			slices.SortFunc(players[start:end], func(a, b Player) int {
				return cmp.Or(cmp.Compare(b.Rating, a.Rating), cmp.Compare(a.ID, b.ID))
			})
		}
		start = end
	}
}

func rawTransfer(upper, lower Player) float64 {
	score := 1.0
	if drew(upper, lower) {
		score = 0.5
	}
	return kFactor * (score - expectedScore(upper.Rating, lower.Rating))
}

func expectedScore(ratingA, ratingB float64) float64 {
	return 1.0 / (1.0 + math.Pow(10.0, (ratingB-ratingA)/400.0))
}

func capTransfer(moved, gainsRating, losesRating float64) float64 {
	if moved < 0 {
		return -min(-moved, max(MaxRating-losesRating, 0), max(gainsRating-MinRating, 0))
	}
	return min(moved, max(MaxRating-gainsRating, 0), max(losesRating-MinRating, 0))
}

func drew(a, b Player) bool {
	return a.Place != 0 && a.Place == b.Place
}
