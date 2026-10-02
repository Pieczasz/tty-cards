package deck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllRanks_ListsEveryRankOnceInOrder(t *testing.T) {
	t.Parallel()

	seen := make(map[Rank]bool, len(AllRanks))
	for i, rank := range AllRanks {
		require.Falsef(t, seen[rank], "AllRanks lists %d twice", rank)
		seen[rank] = true
		if i > 0 {
			assert.Greaterf(t, rank, AllRanks[i-1], "AllRanks[%d] is out of order", i)
		}
	}

	for rank := Ace; rank <= Joker; rank++ {
		assert.Truef(t, seen[rank], "AllRanks is missing standard rank %d", rank)
	}
	for rank := Zero; rank <= WildDrawFour; rank++ {
		assert.Truef(t, seen[rank], "AllRanks is missing Uno rank %d", rank)
	}
	assert.Len(t, AllRanks, int(Joker-Ace+1)+int(WildDrawFour-Zero+1))
}

// A zero Card must not be mistakable for a real one: the standard ranks start at 1
// precisely so that Card{} is detectably empty rather than the ace of spades.
func TestZeroRankIsNotACard(t *testing.T) {
	t.Parallel()
	var empty Card
	assert.NotEqual(t, Ace, empty.Rank)
	assert.Zero(t, RankValue(empty.Rank))
	assert.Zero(t, RunOrder(empty.Rank))
	assert.Zero(t, PipValue(empty.Rank))
	assert.NotContains(t, Standard(), empty)
}

// The zero Suit must be NoSuit for the same reason: a Card built without one is
// suitless, not a spade.
func TestZeroSuitIsUnset(t *testing.T) {
	t.Parallel()
	var empty Card
	assert.Equal(t, NoSuit, empty.Suit)
	for _, suit := range []Suit{Spades, Hearts, Diamonds, Clubs} {
		assert.NotEqual(t, NoSuit, suit)
	}
}

func TestRankValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rank Rank
		want int
	}{
		{name: "ace is high", rank: Ace, want: 14},
		{name: "two is lowest", rank: Two, want: 2},
		{name: "ten", rank: Ten, want: 10},
		{name: "jack", rank: Jack, want: 11},
		{name: "king", rank: King, want: 13},
		{name: "the joker does not rank", rank: Joker, want: 0},
		{name: "uno zero has no ordering", rank: Zero, want: 0},
		{name: "uno one has no ordering", rank: One, want: 0},
		{name: "wild draw four has no ordering", rank: WildDrawFour, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, RankValue(tt.rank))
		})
	}
}

// The three rank scales exist because they answer different questions, and the one
// thing they must agree on is which ranks they have no answer for: a rank no scale
// places is a card no game deals.
func TestRankScalesAgreeOnWhatIsNotAStandardCard(t *testing.T) {
	t.Parallel()

	for _, rank := range AllRanks {
		if rank >= Ace && rank <= King {
			assert.Positivef(t, RankValue(rank), "%d is a standard card", rank)
			continue
		}
		assert.Zerof(t, RankValue(rank), "RankValue(%d)", rank)
		assert.Zerof(t, RunOrder(rank), "RunOrder(%d)", rank)
		assert.Zerof(t, PipValue(rank), "PipValue(%d)", rank)
	}
}

func TestRankValue_StandardRanksAreStrictlyIncreasing(t *testing.T) {
	t.Parallel()
	ordered := []Rank{Two, Three, Four, Five, Six, Seven, Eight, Nine, Ten, Jack, Queen, King, Ace}
	for i := 1; i < len(ordered); i++ {
		assert.Greater(t, RankValue(ordered[i]), RankValue(ordered[i-1]),
			"%v must outrank %v", ordered[i], ordered[i-1])
	}
}

func TestRemoveOne(t *testing.T) {
	t.Parallel()
	aceSpades := Card{Rank: Ace, Suit: Spades}
	kingHearts := Card{Rank: King, Suit: Hearts}

	t.Run("removes a single copy and keeps order", func(t *testing.T) {
		t.Parallel()
		hand := []Card{kingHearts, aceSpades, kingHearts}
		assert.Equal(t, []Card{kingHearts, kingHearts}, RemoveOne(hand, aceSpades))
	})

	t.Run("leaves duplicates behind", func(t *testing.T) {
		t.Parallel()
		hand := []Card{kingHearts, kingHearts}
		assert.Equal(t, []Card{kingHearts}, RemoveOne(hand, kingHearts))
	})

	t.Run("a card the hand does not hold changes nothing", func(t *testing.T) {
		t.Parallel()
		hand := []Card{kingHearts}
		assert.Equal(t, hand, RemoveOne(hand, aceSpades))
	})

	t.Run("an empty hand does not panic", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, RemoveOne(nil, aceSpades))
		assert.Empty(t, RemoveOne([]Card{}, aceSpades))
	})

	t.Run("does not alias or mutate the input", func(t *testing.T) {
		t.Parallel()
		hand := []Card{kingHearts, aceSpades}
		got := RemoveOne(hand, kingHearts)
		got[0] = Card{Rank: Two, Suit: Clubs}
		assert.Equal(t, []Card{kingHearts, aceSpades}, hand)
	})
}

func TestRemoveEach(t *testing.T) {
	t.Parallel()
	two := Card{Rank: Two, Suit: Clubs}
	three := Card{Rank: Three, Suit: Clubs}
	four := Card{Rank: Four, Suit: Clubs}

	t.Run("removes one copy of each", func(t *testing.T) {
		t.Parallel()
		hand := []Card{two, three, four, two}
		assert.Equal(t, []Card{three, two}, RemoveEach(hand, []Card{two, four}))
	})

	t.Run("removing nothing keeps the hand", func(t *testing.T) {
		t.Parallel()
		hand := []Card{two, three}
		assert.Equal(t, hand, RemoveEach(hand, nil))
	})

	t.Run("an empty hand does not panic", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, RemoveEach(nil, []Card{two}))
	})

	t.Run("does not mutate the input", func(t *testing.T) {
		t.Parallel()
		hand := []Card{two, three, four}
		RemoveEach(hand, []Card{three})
		assert.Equal(t, []Card{two, three, four}, hand)
	})
}

func TestRunOrderAndPipValueDivergeOnCourtCards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		rank              Rank
		wantRun, wantPips int
	}{
		{name: "ace is low in both", rank: Ace, wantRun: 1, wantPips: 1},
		{name: "pip card matches", rank: Nine, wantRun: 9, wantPips: 9},
		{name: "ten matches", rank: Ten, wantRun: 10, wantPips: 10},
		{name: "jack", rank: Jack, wantRun: 11, wantPips: 10},
		{name: "queen", rank: Queen, wantRun: 12, wantPips: 10},
		{name: "king", rank: King, wantRun: 13, wantPips: 10},
		{name: "joker has no run position", rank: Joker, wantRun: 0, wantPips: 0},
		{name: "uno rank has neither", rank: WildDrawFour, wantRun: 0, wantPips: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantRun, RunOrder(tt.rank), "RunOrder")
			assert.Equal(t, tt.wantPips, PipValue(tt.rank), "PipValue")
		})
	}

	t.Run("court cards stay consecutive as a run", func(t *testing.T) {
		t.Parallel()
		for _, pair := range [][2]Rank{{Ten, Jack}, {Jack, Queen}, {Queen, King}} {
			assert.Equal(t, RunOrder(pair[0])+1, RunOrder(pair[1]),
				"%d must follow %d", pair[1], pair[0])
		}
	})
}
