package poker

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/poker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildSeats is the one place the view decides whose cards a player may see, and it
// reaches past BoundEngine into whole-table state to do it. Every branch of that
// decision is a hand somebody could otherwise read for free, so each one is pinned
// here rather than left to whichever branch the engine happens to walk.
func TestBuildSeats_RevealsHoleCardsOnlyWhereTheRulesDo(t *testing.T) {
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
	newExtra := func() *logic.State {
		return &logic.State{
			Seats: map[string]*logic.Seat{
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

			seats := buildSeats(state, extra, "hero")
			require.Len(t, seats, 3)

			byID := map[string]seat{}
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

func assertHole(t *testing.T, s seat, want []deck.Card, revealed bool, who string) {
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
func TestBuildSeats_CopiesTheHoleCardsItReveals(t *testing.T) {
	t.Parallel()

	cards := []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}, {Rank: deck.King, Suit: deck.Spades}}
	state := &game.State{
		Phase:   game.Finished,
		Players: []*game.Player{{ID: "hero", Name: "alice", Cards: cards}},
	}
	extra := &logic.State{}

	seats := buildSeats(state, extra, "hero")
	require.Len(t, seats, 1)

	cards[0] = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
	assert.Equal(t, deck.Ace, seats[0].Hole[0].Rank, "the seat holds its own copy")
}

// A seat that left mid-hand is a nil entry in State.Players. Skipping it is what keeps
// the table from panicking on a disconnect the engine has not finished cleaning up.
func TestBuildSeats_SkipsAnEmptySeat(t *testing.T) {
	t.Parallel()

	state := &game.State{
		Phase:   game.Playing,
		Players: []*game.Player{{ID: "hero", Name: "alice"}, nil},
	}
	extra := &logic.State{}

	assert.Len(t, buildSeats(state, extra, "hero"), 1)
}
