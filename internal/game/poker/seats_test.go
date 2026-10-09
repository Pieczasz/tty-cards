package poker

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// SeatsFor is the one place that decides whose cards a player may see, from
// whole-table state. Every branch of that decision is a hand somebody could otherwise
// read for free, so each one is pinned here rather than left to whichever branch the
// engine happens to walk.
func TestSeatsFor_RevealsHoleCardsOnlyWhereTheRulesDo(t *testing.T) {
	t.Parallel()

	hole := func(a, b deck.Card) []deck.Card { return []deck.Card{a, b} }
	heroCards := hole(deck.Card{Rank: deck.Ace, Suit: deck.Spades}, deck.Card{Rank: deck.King, Suit: deck.Spades})
	villainCards := hole(deck.Card{Rank: deck.Two, Suit: deck.Clubs}, deck.Card{Rank: deck.Three, Suit: deck.Clubs})
	foldedCards := hole(deck.Card{Rank: deck.Four, Suit: deck.Hearts}, deck.Card{Rank: deck.Five, Suit: deck.Hearts})

	newState := func(phase game.Phase) *game.State {
		return &game.State{
			Phase: phase,
			Players: []*game.Player{
				{ID: "hero", Name: "alice", Cards: heroCards},
				{ID: "villain", Name: "bob", Cards: villainCards},
				{ID: "folder", Name: "carol", Cards: foldedCards},
			},
		}
	}
	newExtra := func() *State {
		return &State{
			Seats: map[string]*Seat{
				"hero": {Chips: 100},
				// Both non-hero seats have their stack in the middle: an all-in run-out
				// is exactly the case where the cards go face up before anyone acts again.
				"villain": {Chips: 100, AllIn: true},
				"folder":  {Folded: true},
			},
		}
	}

	tests := []struct {
		name        string
		phase       game.Phase
		showdown    bool
		wantVillain bool
		wantFolder  bool
	}{
		{
			name:  "mid-hand nobody but the hero sees a card",
			phase: game.Playing,
		},
		{
			name:        "a showdown turns the live hands up",
			phase:       game.Playing,
			showdown:    true,
			wantVillain: true,
		},
		{
			name:     "a seat that folded into the showdown still shows nothing",
			phase:    game.Playing,
			showdown: true,
			// wantFolder stays false: mucking is the whole point of folding, and with
			// hands still to play a free read is worth real chips.
			wantVillain: true,
		},
		{
			name:        "the all-in run-out is a showdown like any other",
			phase:       game.Playing,
			showdown:    true,
			wantVillain: true,
		},
		{
			name:        "a final hand that was shown down reveals whoever was still live",
			phase:       game.Finished,
			showdown:    true,
			wantVillain: true,
		},
		{
			// The match being over is not a showdown: a last pot won face-down keeps
			// the winner's cards hidden like any other.
			name:  "a final hand won face-down shows no winner cards",
			phase: game.Finished,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state, extra := newState(tt.phase), newExtra()
			extra.ReachedShowdown = tt.showdown

			state.Extra = extra
			seats := SeatsFor(state, "hero")
			require.Len(t, seats, 3)

			byID := map[string]SeatView{}
			for _, s := range seats {
				byID[s.PlayerID] = s
			}

			assert.Equal(t, heroCards, byID["hero"].Hole, "the hero always sees their own cards")
			assertHole(t, byID["villain"], villainCards, tt.wantVillain, "the live opponent")
			assertHole(t, byID["folder"], foldedCards, tt.wantFolder, "the folded seat")

			// Whatever is hidden, the hand size still has to be right or the table
			// draws a seat holding cards it was never dealt.
			for _, s := range seats {
				assert.Equal(t, 2, s.HandSize, "%s was dealt two cards", s.Name)
			}
		})
	}
}

func assertHole(t *testing.T, s SeatView, want []deck.Card, revealed bool, who string) {
	t.Helper()
	if revealed {
		assert.Equal(t, want, s.Hole, "%s is shown down", who)
		return
	}
	assert.Empty(t, s.Hole, "%s must not leak a hole card", who)
}

// The revealed cards are copied out of the engine's own player, so a later deal into
// the same backing array cannot rewrite a hand the view is still drawing - under -race
// reading it at all would be a race.
func TestSeatsFor_CopiesTheHoleCardsItReveals(t *testing.T) {
	t.Parallel()

	cards := []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}, {Rank: deck.King, Suit: deck.Spades}}
	state := &game.State{
		Phase:   game.Finished,
		Players: []*game.Player{{ID: "hero", Name: "alice", Cards: cards}},
		Extra:   &State{},
	}

	seats := SeatsFor(state, "hero")
	require.Len(t, seats, 1)

	cards[0] = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
	assert.Equal(t, deck.Ace, seats[0].Hole[0].Rank, "the seat holds its own copy")
}

// A seat that left mid-hand is a nil entry in State.Players. Skipping it is what keeps
// the table from panicking on a disconnect the engine has not finished cleaning up.
func TestSeatsFor_SkipsAnEmptySeat(t *testing.T) {
	t.Parallel()

	state := &game.State{
		Phase:   game.Playing,
		Players: []*game.Player{{ID: "hero", Name: "alice"}, nil},
		Extra:   &State{},
	}

	assert.Len(t, SeatsFor(state, "hero"), 1)
}

// A view calls SeatsFor inside Frame, which has no recover: a table whose Extra is not
// poker's yet has no seats to show rather than a panic.
func TestSeatsFor_AStateThatIsNotPokersHasNoSeats(t *testing.T) {
	t.Parallel()
	state := &game.State{Players: []*game.Player{{ID: "hero"}}}

	assert.Empty(t, SeatsFor(state, "hero"))
}

// Whatever the betting did, the viewer sees their own cards, nobody's before a
// showdown, and never a folded seat's.
func TestSeatsFor_RevealsNoOtherHandBeforeAShowdownAndNoFoldedHandEver(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(MinPlayers, MaxPlayers).Draw(t, "players")
		stock := deck.New(deck.Standard())
		stock.Shuffle()
		state := &game.State{Phase: game.Playing}
		extra := &State{Seats: map[string]*Seat{}, ReachedShowdown: rapid.Bool().Draw(t, "showdown")}
		for i := range n {
			id := fmt.Sprintf("p%d", i+1)
			hole, _ := stock.DrawN(2)
			state.Players = append(state.Players, &game.Player{ID: id, Cards: hole})
			extra.Seats[id] = &Seat{Folded: rapid.Bool().Draw(t, "folded "+id), AllIn: rapid.Bool().Draw(t, "all-in "+id)}
		}
		state.Extra = extra
		viewer := rapid.SampledFrom(state.Players).Draw(t, "viewer").ID

		for i, s := range SeatsFor(state, viewer) {
			cards := state.Players[i].Cards
			switch {
			case s.PlayerID == viewer:
				require.Equal(t, cards, s.Hole, "the viewer sees their own hand")
			case !extra.ReachedShowdown:
				require.Empty(t, s.Hole, "%s's hand before any showdown", s.PlayerID)
			case s.Folded:
				require.Empty(t, s.Hole, "%s folded and mucked", s.PlayerID)
			default:
				require.Equal(t, cards, s.Hole, "%s is shown down", s.PlayerID)
			}
		}
	})
}
