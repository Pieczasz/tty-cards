package catalog

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"

	crazyeightrules "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	ginrummyrules "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	heartsrules "github.com/Pieczasz/tty-cards/internal/game/hearts"
	pokerrules "github.com/Pieczasz/tty-cards/internal/game/poker"
	unorules "github.com/Pieczasz/tty-cards/internal/game/uno"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

type moveFunc = func(t *rapid.T, s *game.State) []game.Action

// moves is each game's candidate moves for the shared Rules contract, by slug. They
// live here rather than beside each game's rules because a package's own tests cannot
// be imported, and the catalog already imports every rules package: so registering a
// game without them fails the contract below instead of skipping it.
var moves = map[string]moveFunc{
	"crazy_eights": shedMoves(
		func(c deck.Card, suit deck.Suit) game.Action {
			return crazyeightrules.ActionPlayCard{Card: c, ChosenSuit: suit}
		},
		crazyeightrules.ActionDrawCard{},
	),
	"uno": shedMoves(
		func(c deck.Card, suit deck.Suit) game.Action {
			return unorules.ActionPlayCard{Card: c, ChosenSuit: suit}
		},
		unorules.ActionDrawCard{},
	),
	"poker":     pokerMoves,
	"hearts":    heartsMoves,
	"gin_rummy": ginRummyMoves,
}

func catalogGames(t *testing.T) []gametest.Game {
	t.Helper()
	out := make([]gametest.Game, 0, len(Games))
	for _, e := range Games {
		m, ok := moves[e.Slug]
		require.Truef(t, ok, "%q has no candidate moves for the Rules contract", e.Slug)
		out = append(out, gametest.Game{Module: e.Module, Moves: m})
	}
	return out
}

func TestGames_KeepTheRulesContract(t *testing.T) {
	t.Parallel()
	for _, g := range catalogGames(t) {
		t.Run(g.Slug, func(t *testing.T) {
			t.Parallel()
			gametest.RunRules(t, g)
		})
	}
}

func TestGames_TimeoutActionIsLegalAmongRandomMoves(t *testing.T) {
	t.Parallel()
	for _, g := range catalogGames(t) {
		t.Run(g.Slug, func(t *testing.T) {
			t.Parallel()
			gametest.SoakAmongRandomMoves(t, g)
		})
	}
}

// shedMoves is every card played once, naming a suit only where the plain play is
// refused - the Eight's or the Wild's choice - and a draw.
func shedMoves(play func(deck.Card, deck.Suit) game.Action, draw game.Action) moveFunc {
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	return func(_ *rapid.T, s *game.State) []game.Action {
		out := []game.Action{draw}
		for _, c := range s.Players[s.CurrentTurn].Cards {
			if plain := play(c, deck.NoSuit); s.Rules.ValidateAction(s, plain) == nil {
				out = append(out, plain)
				continue
			}
			for _, suit := range suits {
				out = append(out, play(c, suit))
			}
		}
		return out
	}
}

// pokerMoves bets both ends of the raise band, so short all-ins and raises that do
// not reopen the betting come up.
func pokerMoves(_ *rapid.T, s *game.State) []game.Action {
	lo, hi, _ := pokerrules.RaiseBounds(s, s.Players[s.CurrentTurn].ID)
	return []game.Action{
		pokerrules.ActionCheck{},
		pokerrules.ActionCall{},
		pokerrules.ActionRaiseTo{Amount: lo},
		pokerrules.ActionRaiseTo{Amount: hi},
		pokerrules.ActionAllIn{},
		pokerrules.ActionFold{},
		pokerrules.ActionNextHand{},
	}
}

// heartsMoves passes three random cards, the pass a real player makes rather than the
// clock's three most dangerous, and plays every card.
func heartsMoves(t *rapid.T, s *game.State) []game.Action {
	hand := s.Players[s.CurrentTurn].Cards
	out := []game.Action{heartsrules.ActionNextHand{}}
	if len(hand) >= 3 {
		pass := rapid.Permutation(hand).Draw(t, "pass")[:3]
		out = append(out, heartsrules.ActionPassCards{Cards: pass})
	}
	for _, c := range hand {
		out = append(out, heartsrules.ActionPlayCard{Card: c})
	}
	return out
}

// ginRummyMoves draws from both piles, so the taken upcard autoDiscard must not lay
// straight back comes up, discards every card, and knocks on one: each knock costs a
// meld search, and one is enough to end hands early now and then.
func ginRummyMoves(t *rapid.T, s *game.State) []game.Action {
	hand := s.Players[s.CurrentTurn].Cards
	out := []game.Action{ginrummyrules.ActionDrawStock{}, ginrummyrules.ActionDrawDiscard{}}
	for _, c := range hand {
		out = append(out, ginrummyrules.ActionDiscard{Card: c})
	}
	if len(hand) > 0 {
		knock := rapid.SampledFrom(hand).Draw(t, "knock")
		out = append(out, ginrummyrules.ActionKnock{Discard: knock})
	}
	return out
}
