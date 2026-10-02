package shed

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shedState(stock, discard []deck.Card) *game.State {
	return &game.State{
		Players: []*game.Player{{ID: "p1", Cards: []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}}}},
		Deck:    deck.New(stock),
		Discard: deck.New(discard),
	}
}

func shedCardsInPlay(state *game.State) int {
	total := state.Deck.Size() + state.Discard.Size()
	for _, p := range state.Players {
		total += len(p.Cards)
	}
	return total
}

func TestReshuffleDiscardIntoStock(t *testing.T) {
	t.Parallel()
	discard := []deck.Card{
		{Rank: deck.Two, Suit: deck.Hearts},
		{Rank: deck.Three, Suit: deck.Clubs},
		{Rank: deck.Four, Suit: deck.Diamonds},
	}

	t.Run("refills the stock and conserves every card", func(t *testing.T) {
		t.Parallel()
		state := shedState(nil, discard)
		before := shedCardsInPlay(state)
		top, _ := state.Discard.Peek()

		reshuffleDiscardIntoStock(state)

		assert.Equal(t, before, shedCardsInPlay(state))
		assert.Equal(t, 1, state.Discard.Size(), "the card in play stays in play")
		nowTop, _ := state.Discard.Peek()
		assert.Equal(t, top, nowTop)
		assert.Equal(t, len(discard)-1, state.Deck.Size())
	})

	// Merging into a stock still in play would lose its order, so a caller that
	// reshuffles too early gets nothing done rather than a shuffled stock.
	t.Run("leaves a stock that is not empty alone", func(t *testing.T) {
		t.Parallel()
		stock := []deck.Card{{Rank: deck.Five, Suit: deck.Spades}}
		state := shedState(stock, discard)

		reshuffleDiscardIntoStock(state)

		assert.Equal(t, stock, state.Deck.Cards())
		assert.Equal(t, discard, state.Discard.Cards())
	})

	t.Run("an empty discard is a no-op", func(t *testing.T) {
		t.Parallel()
		state := shedState(nil, nil)
		reshuffleDiscardIntoStock(state)
		assert.Equal(t, 1, shedCardsInPlay(state))
	})
}

func TestValidatePlay(t *testing.T) {
	t.Parallel()
	held := deck.Card{Rank: deck.Ace, Suit: deck.Spades} // shedState deals p1 this card
	top := deck.Card{Rank: deck.Two, Suit: deck.Hearts}
	matchSaw := func(saw *deck.Card) func(deck.Card) error {
		return func(c deck.Card) error { *saw = c; return nil }
	}

	t.Run("no card in play refuses before the game's rule runs", func(t *testing.T) {
		t.Parallel()
		var saw deck.Card
		require.ErrorContains(t, ValidatePlay(shedState(nil, nil), held, matchSaw(&saw)), "no cards in discard")
		assert.Zero(t, saw)
	})
	t.Run("a card not in the hand is refused", func(t *testing.T) {
		t.Parallel()
		var saw deck.Card
		err := ValidatePlay(shedState(nil, []deck.Card{top}), top, matchSaw(&saw))
		require.ErrorContains(t, err, "you don't have that card")
		assert.Zero(t, saw)
	})
	t.Run("the game's rule decides against the top card", func(t *testing.T) {
		t.Parallel()
		var saw deck.Card
		require.NoError(t, ValidatePlay(shedState(nil, []deck.Card{top}), held, matchSaw(&saw)))
		assert.Equal(t, top, saw)
	})
}

func TestDraw(t *testing.T) {
	t.Parallel()
	two := deck.Card{Rank: deck.Two, Suit: deck.Hearts}
	three := deck.Card{Rank: deck.Three, Suit: deck.Clubs}

	tests := []struct {
		name        string
		stock       []deck.Card
		discard     []deck.Card
		want        deck.Card
		wantOK      bool
		wantDiscard []deck.Card
	}{
		{name: "draws off the stock", stock: []deck.Card{two}, discard: []deck.Card{three}, want: two, wantOK: true, wantDiscard: []deck.Card{three}},
		{name: "refills an empty stock from under the top card", discard: []deck.Card{two, three}, want: two, wantOK: true, wantDiscard: []deck.Card{three}},
		{name: "a one-card discard leaves nothing to draw", discard: []deck.Card{three}, wantDiscard: []deck.Card{three}},
		{name: "both piles empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := shedState(tt.stock, tt.discard)
			before := shedCardsInPlay(state)

			got, ok := draw(state)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantDiscard, state.Discard.Cards(), "the card in play never moves")
			if ok {
				before--
			}
			assert.Equal(t, before, shedCardsInPlay(state), "every card is conserved")
		})
	}
}

func TestReturnHandToStock(t *testing.T) {
	t.Parallel()

	t.Run("the leaver's cards go back to the stock", func(t *testing.T) {
		t.Parallel()
		state := shedState([]deck.Card{{Rank: deck.Two, Suit: deck.Clubs}}, nil)
		state.Players = append(state.Players, &game.Player{ID: "p2", Cards: []deck.Card{
			{Rank: deck.King, Suit: deck.Hearts},
			{Rank: deck.Queen, Suit: deck.Hearts},
		}})
		before := shedCardsInPlay(state)

		returnHandToStock(state, "p2")

		assert.Equal(t, before, shedCardsInPlay(state))
		assert.Equal(t, 3, state.Deck.Size())
		assert.Empty(t, state.Players[1].Cards)
		assert.Len(t, state.Players[0].Cards, 1, "everyone else keeps their hand")
	})

	t.Run("an unknown player changes nothing", func(t *testing.T) {
		t.Parallel()
		state := shedState(nil, nil)
		before := shedCardsInPlay(state)
		returnHandToStock(state, "nobody")
		assert.Equal(t, before, shedCardsInPlay(state))
	})
}

func TestHandEmptyOrAllPassed(t *testing.T) {
	t.Parallel()
	seated := func(counts ...int) *game.State {
		state := &game.State{}
		for i, n := range counts {
			state.Players = append(state.Players, &game.Player{
				ID:    string(rune('a' + i)),
				Cards: make([]deck.Card, n),
			})
		}
		return state
	}

	tests := []struct {
		name   string
		state  *game.State
		passes int
		want   bool
	}{
		{name: "an empty hand wins", state: seated(0, 3), want: true},
		{name: "a live hand carries on", state: seated(3, 5, 1), want: false},
		{name: "fewer passes than seats is not a deadlock", state: seated(2, 2, 2), passes: 2},
		{name: "one pass per seat ends the hand", state: seated(2, 2, 2), passes: 3, want: true},
		{name: "an empty table is not a deadlock", state: seated(), passes: 3, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, HandEmptyOrAllPassed(tt.state, tt.passes))
		})
	}
}

func TestOpenDiscard(t *testing.T) {
	t.Parallel()
	// Draw reads the end of the pile, so the last card listed is the first drawn.
	stock := []deck.Card{
		{Rank: deck.Five, Suit: deck.Clubs},
		{Rank: deck.Nine, Suit: deck.Hearts},
		{Rank: deck.Eight, Suit: deck.Spades},
		{Rank: deck.Eight, Suit: deck.Diamonds},
	}
	notAnEight := func(c deck.Card) bool { return c.Rank != deck.Eight }

	t.Run("the first legal card opens the pile", func(t *testing.T) {
		t.Parallel()
		state := shedState([]deck.Card{{Rank: deck.Four, Suit: deck.Hearts}}, nil)
		before := shedCardsInPlay(state)

		top, err := OpenDiscard(state, notAnEight)

		require.NoError(t, err)
		assert.Equal(t, deck.Card{Rank: deck.Four, Suit: deck.Hearts}, top)
		assert.Equal(t, 1, state.Discard.Size())
		assert.True(t, state.Deck.IsEmpty())
		assert.Equal(t, before, shedCardsInPlay(state))
	})

	t.Run("the cards it refuses go back into the stock", func(t *testing.T) {
		t.Parallel()
		state := shedState(stock, nil)
		before := shedCardsInPlay(state)

		top, err := OpenDiscard(state, notAnEight)

		require.NoError(t, err)
		assert.Equal(t, deck.Card{Rank: deck.Nine, Suit: deck.Hearts}, top,
			"both eights are passed over")
		assert.Equal(t, 3, state.Deck.Size(), "the eights are dealt, not discarded")
		assert.Equal(t, before, shedCardsInPlay(state))
	})

	// A pile that opens on a card nobody can match is worse than a table that refuses
	// to start, so the error path has to leave the stock whole for the caller to report.
	t.Run("a stock with no legal opener errors and conserves the cards", func(t *testing.T) {
		t.Parallel()
		state := shedState([]deck.Card{
			{Rank: deck.Eight, Suit: deck.Spades},
			{Rank: deck.Eight, Suit: deck.Clubs},
		}, nil)
		before := shedCardsInPlay(state)

		_, err := OpenDiscard(state, notAnEight)

		require.Error(t, err)
		assert.Equal(t, before, shedCardsInPlay(state))
		assert.Equal(t, 2, state.Deck.Size())
		assert.True(t, state.Discard.IsEmpty(), "a refused start leaves nothing in play")
	})
}

func TestStandings_FewestCardsFirstAndTiesStable(t *testing.T) {
	t.Parallel()
	state := &game.State{Players: []*game.Player{
		{ID: "p1", Cards: make([]deck.Card, 4)},
		{ID: "p2", Cards: make([]deck.Card, 1)},
		{ID: "p3", Cards: make([]deck.Card, 4)},
	}}

	standings := Standings(state)

	require.Len(t, standings, 3)
	assert.Equal(t, []string{"p2", "p1", "p3"}, []string{standings[0].ID, standings[1].ID, standings[2].ID},
		"ties keep seat order so the ranking is reproducible")
	assert.Equal(t, Score(standings[1]), Score(standings[2]),
		"Standings and StandingScore have to agree, or a draw is split by seat")
}

func TestLeave(t *testing.T) {
	t.Parallel()
	state := shedState(nil, nil)
	state.Players = append(state.Players, &game.Player{ID: "p2", Cards: []deck.Card{
		{Rank: deck.King, Suit: deck.Hearts},
	}})
	shed := &State{Passes: 2}
	before := shedCardsInPlay(state)

	Leave(state, shed, "p2")

	assert.Zero(t, shed.Passes, "the count measured a table that no longer exists")
	assert.Equal(t, before, shedCardsInPlay(state))
	assert.Empty(t, state.Players[1].Cards)
}

func TestDrawInto_DealsWhatTheStockHas(t *testing.T) {
	t.Parallel()
	p := &game.Player{ID: "p1"}
	state := &game.State{Players: []*game.Player{p}, Deck: deck.New([]deck.Card{{Rank: deck.Ace}, {Rank: deck.Two}}), Discard: deck.New(nil)}

	assert.True(t, DrawInto(state, p, 3), "two of three is still a draw")
	assert.Len(t, p.Cards, 2)
	assert.False(t, DrawInto(state, p, 1), "an empty stock and discard yield nothing")
}

func TestState_RecordDraw(t *testing.T) {
	t.Parallel()
	s := &State{}
	s.RecordDraw(false)
	s.RecordDraw(false)
	assert.Equal(t, 2, s.Passes)
	s.RecordDraw(true)
	assert.Equal(t, 0, s.Passes)
}
