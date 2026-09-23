package deck

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A shuffle has to be a permutation and it has to be uniform. Contains-based checks
// see neither: a shuffle that duplicated a card or only ever rotated the pile would
// pass them.
func TestPile_Shuffle_IsAPermutation(t *testing.T) {
	t.Parallel()
	cards := Standard()
	p := New(cards)

	p.Shuffle()

	got := p.Cards()
	require.Len(t, got, len(cards))
	sort := func(in []Card) []Card {
		out := slices.Clone(in)
		slices.SortFunc(out, func(a, b Card) int {
			if d := int(a.Suit) - int(b.Suit); d != 0 {
				return d
			}
			return int(a.Rank) - int(b.Rank)
		})
		return out
	}
	assert.Equal(t, sort(cards), sort(got), "the same cards, each exactly once")
}

// Three cards have six orderings, and a biased shuffle shows up as one of them being
// rare. A Fisher-Yates that drew its index from the wrong range is the usual way to
// get this wrong, and it still returns a permutation every time.
func TestPile_Shuffle_HitsEveryPermutation(t *testing.T) {
	t.Parallel()
	const rounds = 30000
	cards := []Card{{Rank: Ace, Suit: Spades}, {Rank: King, Suit: Hearts}, {Rank: Queen, Suit: Diamonds}}

	counts := map[string]int{}
	for range rounds {
		p := New(cards)
		p.Shuffle()
		key := fmt.Sprint(p.Cards())
		counts[key]++
	}

	require.Len(t, counts, 6, "every ordering must be reachable, got %v", counts)
	expected := rounds / 6
	for order, n := range counts {
		assert.InEpsilon(t, expected, n, 0.10, "ordering %s came up %d times", order, n)
	}
}

func TestPile_Peek(t *testing.T) {
	t.Parallel()
	cards := []Card{{Rank: Ace, Suit: Spades}}
	p := &Pile{cards: cards}

	gotCard, gotOk := p.Peek()
	assert.True(t, gotOk)
	assert.Equal(t, Card{Rank: Ace, Suit: Spades}, gotCard)

	gotSize := p.Size()
	assert.Equal(t, 1, gotSize)

	pEmpty := &Pile{}
	_, gotEmptyOk := pEmpty.Peek()
	assert.False(t, gotEmptyOk)
}

func TestPile_Draw(t *testing.T) {
	t.Parallel()
	cards := []Card{{Rank: Ace, Suit: Spades}, {Rank: King, Suit: Hearts}}
	p := &Pile{cards: cards}

	gotCard, gotOk := p.Draw()
	assert.True(t, gotOk)
	assert.Equal(t, Card{Rank: King, Suit: Hearts}, gotCard)

	gotSize := p.Size()
	assert.Equal(t, 1, gotSize)

	p.Draw()
	_, gotEmptyOk := p.Draw()
	assert.False(t, gotEmptyOk)
}

func TestPile_DrawN(t *testing.T) {
	t.Parallel()
	cards := []Card{{Rank: Ace, Suit: Spades}, {Rank: Two, Suit: Spades}, {Rank: Three, Suit: Spades}}
	p := &Pile{cards: cards}

	gotCards, gotOk := p.DrawN(2)
	assert.True(t, gotOk)
	assert.Len(t, gotCards, 2)

	want := []Card{{Rank: Three, Suit: Spades}, {Rank: Two, Suit: Spades}}
	assert.Equal(t, want, gotCards)

	assert.Equal(t, 1, p.Size())

	_, gotOkFail := p.DrawN(5)
	assert.False(t, gotOkFail)
}

func TestPile_Add(t *testing.T) {
	t.Parallel()
	ace := Card{Rank: Ace, Suit: Spades}
	king := Card{Rank: King, Suit: Hearts}

	p := &Pile{}
	p.Add(ace)
	p.Add(king, ace)

	assert.Equal(t, []Card{ace, king, ace}, p.Cards(),
		"cards go on top in the order they were added, duplicates and all")
}

// Size and IsEmpty are what the shedding rules read to decide whether to reshuffle the
// discard back in, so an empty pile has to agree with itself both ways.
func TestPile_IsEmptyTracksSize(t *testing.T) {
	t.Parallel()

	p := New(nil)
	assert.True(t, p.IsEmpty())
	assert.Zero(t, p.Size())

	p.Add(Card{Rank: Ace, Suit: Spades})
	assert.False(t, p.IsEmpty())

	_, ok := p.Draw()
	require.True(t, ok)
	assert.True(t, p.IsEmpty(), "drawing the last card empties the pile")
}

// New and Cards are the seams the engine hands piles across; both must copy, or a
// rules set shuffling the deck would reorder the slice its caller still holds.
func TestPile_DoesNotAliasTheCallersSlice(t *testing.T) {
	t.Parallel()
	ace := Card{Rank: Ace, Suit: Spades}
	cards := []Card{ace, {Rank: King, Suit: Hearts}}

	p := New(cards)
	p.Add(Card{Rank: Two, Suit: Clubs})
	assert.Len(t, cards, 2, "New must copy")

	out := p.Cards()
	out[0] = Card{}
	got, ok := p.Draw()
	require.True(t, ok)
	assert.Equal(t, Card{Rank: Two, Suit: Clubs}, got)
	assert.Equal(t, []Card{ace, {Rank: King, Suit: Hearts}}, p.Cards(), "Cards must copy")
}

func FuzzPile_DrawN(f *testing.F) {
	f.Add(0, 0)
	f.Add(3, -1)
	f.Add(3, 5)
	f.Add(52, 52)

	f.Fuzz(func(t *testing.T, size, want int) {
		if size < 0 || size > 512 {
			t.Skip()
		}
		// Real ranks only: the zero Rank is deliberately nobody's card, so dealing
		// it would test a pile of cards no deck can hold.
		cards := make([]Card, 0, size)
		for i := range size {
			cards = append(cards, Card{Rank: AllRanks[i%len(AllRanks)], Suit: Suit(i%4) + Spades})
		}
		p := New(cards)

		got, ok := p.DrawN(want)
		if !ok {
			assert.Empty(t, got, "a refused draw yields no cards")
			assert.Equal(t, size, p.Size(), "a refused draw leaves the pile untouched")
			return
		}
		assert.Len(t, got, want, "a successful draw yields exactly the requested count")
		assert.Equal(t, size-want, p.Size(), "the pile shrinks by exactly what was drawn")
	})
}

// DrawN copies rather than reslicing, and the truncated pile keeps its capacity -
// so an aliased hand would be rewritten by the next Add. Both shedding games add
// the set-aside cards back after dealing, which is that exact sequence.
func TestPile_DrawN_DoesNotAliasThePile(t *testing.T) {
	t.Parallel()
	p := New(Standard())

	hand, ok := p.DrawN(5)
	require.True(t, ok)
	dealt := slices.Clone(hand)

	p.Add(Card{Rank: Ace, Suit: Spades}, Card{Rank: King, Suit: Hearts})

	assert.Equal(t, dealt, hand, "Add rewrote a hand that was already dealt")
}

func TestStandard(t *testing.T) {
	t.Parallel()
	cards := Standard()

	assert.Len(t, cards, 52, "standard deck should have exactly 52 cards")

	suitCounts := make(map[Suit]int)

	for _, card := range cards {
		assert.NotEqual(t, Joker, card.Rank, "standard deck should not contain jokers")
		assert.NotEqual(t, NoSuit, card.Suit, "standard deck should not contain cards without suit")
		suitCounts[card.Suit]++
	}

	assert.Equal(t, 13, suitCounts[Spades], "should have 13 spades")
	assert.Equal(t, 13, suitCounts[Hearts], "should have 13 hearts")
	assert.Equal(t, 13, suitCounts[Diamonds], "should have 13 diamonds")
	assert.Equal(t, 13, suitCounts[Clubs], "should have 13 clubs")
}
