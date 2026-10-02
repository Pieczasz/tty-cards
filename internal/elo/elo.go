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

// clampRating bounds a rating to [MinRating, MaxRating].
func clampRating(rating float64) float64 {
	if math.IsNaN(rating) {
		slog.Error("NaN rating clamped to the default", "default", DefaultRating)
		return DefaultRating
	}
	return min(max(rating, MinRating), MaxRating)
}

// ToUint32 is a rating as it is stored: clamped to the scale and rounded.
func ToUint32(rating float64) uint32 {
	return uint32(math.Round(clampRating(rating)))
}

// Player is one seat in a Calculate: who, at what rating, finishing where.
type Player struct {
	ID     string
	Rating float64
	Place  int
	// Provisional marks an account without a track record yet. Identity is a free
	// SSH keypair, so a fresh 1500 is free to mint: an established player never
	// *gains* from a pair with one. They can still lose to one - otherwise seating an
	// alt would freeze a rating in place, and the anti-farm rule becomes a shield.
	// The provisional side always moves, so it converges on real games.
	Provisional bool
}

// expectedScore is the classic Elo win expectancy of A against B.
func expectedScore(ratingA, ratingB float64) float64 {
	return 1.0 / (1.0 + math.Pow(10.0, (ratingB-ratingA)/400.0))
}

// Calculate applies the Simple Multiplayer Elo (SME) algorithm.
// https://www.tckerrigan.com/Misc/Multiplayer_Elo/
// The player slice MUST be sorted by performance, from 1st place (index 0) to last place (index n-1).
//
// IDs must be distinct. Two seats sharing one ID are still played against each other
// - the transfer between them happens - but the results collapse into a single map
// entry and the last one wins, so one of the two rating changes is lost. The return
// shape has nowhere to report that, so it is logged loudly instead; the caller builds
// the slice from a set of accounts and a repeat is a bug there.
//
// The float64 result conserves rating exactly, but the stored value does not: ToUint32
// rounds each player independently, so a three-or-more-seat table can end up a point
// heavier or lighter than it started. Rounding the table as a whole would mean
// deciding whose rating absorbs the remainder, which is a worse answer than a point of
// drift on a 1500-point scale. A pair that includes a provisional account breaks
// conservation deliberately and by much more - see unpaidAgainstProvisional.
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

// unpaidAgainstProvisional zeroes the gain on the established side of a mixed pair.
// This deliberately breaks conservation for that pair - the zero-sum property of Elo
// is worth less than an unfarmable ladder - but only for gains, so a loss to a fresh
// account still costs what it should.
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

func capTransfer(moved, gainsRating, losesRating float64) float64 {
	if moved < 0 {
		return -min(-moved, max(MaxRating-losesRating, 0), max(gainsRating-MinRating, 0))
	}
	return min(moved, max(MaxRating-gainsRating, 0), max(losesRating-MinRating, 0))
}

func drew(a, b Player) bool {
	return a.Place != 0 && a.Place == b.Place
}
