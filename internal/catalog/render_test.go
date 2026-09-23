package catalog

import (
	"fmt"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every game view is checked here rather than in its own package because the bug this
// pins is shared: nothing in the layout enforces its own budget. PadCenter and Place
// both hand overwide content straight through, so a hand or a stack of card backs that
// does not fit is passed to the terminal to wrap, and one wrapped row shifts every row
// under it. Golden files would pin the pixels; this pins the only property that has to
// hold at every size.
func TestGameViews_RenderInsideTheTerminal(t *testing.T) {
	t.Parallel()

	sizes := []struct{ w, h int }{
		{styles.MinWidth, styles.MinHeight}, // the smallest terminal the router admits
		{80, 24},
		{100, 30},
		{120, 40},
	}
	handSizes := []int{7, 13, 25}

	for _, entry := range All {
		rules := entry.Factory()
		for seats := rules.MinPlayers(); seats <= min(rules.MaxPlayers(), 6); seats++ {
			for _, size := range sizes {
				for _, handSize := range handSizes {
					name := fmt.Sprintf("%s/seats=%d/%dx%d/hand=%d",
						entry.Slug, seats, size.w, size.h, handSize)
					t.Run(name, func(t *testing.T) {
						t.Parallel()

						m := seatedView(t, entry, seats, size.w, size.h)
						setHand(m, longHand(handSize))

						out := m.View().Content
						assert.LessOrEqual(t, lg.Width(out), size.w, "the frame is wider than the terminal")
						assert.LessOrEqual(t, lg.Height(out), size.h, "the frame is taller than the terminal")
					})
				}
			}
		}
	}
}

// A view for player A must never print another seat's card faces. Poker has its own
// targeted showdown test; the other four never reveal anybody's hand at all, so the
// check is that no other seat's cards are anywhere in the frame.
func TestGameViews_NeverShowAnotherSeatsCards(t *testing.T) {
	t.Parallel()

	for _, entry := range All {
		if entry.Slug == "poker" {
			continue // showdown reveals by design; poker tests that reveal itself
		}
		t.Run(entry.Slug, func(t *testing.T) {
			t.Parallel()

			rules := entry.Factory()
			engine, m := seatedEngineAndView(t, entry, rules.MinPlayers(), 120, 40)

			hero := heroHand(t, engine, testutil.SeatID(1))
			out := stripANSI(m.View().Content)

			var others []deck.Card
			engine.WithState(func(state *game.State) {
				for _, p := range state.Players {
					if p == nil || p.ID == testutil.SeatID(1) {
						continue
					}
					others = append(others, p.Cards...)
				}
			})
			require.NotEmpty(t, others, "the other seats have to be holding something")

			for _, card := range others {
				if containsCard(hero, card) {
					continue // the hero holds one like it; its glyphs are theirs to show
				}
				assert.NotContains(t, out, cardGlyphs(card),
					"%v belongs to another seat", card)
			}
		})
	}
}

// cardGlyphs is the rank-and-suit pair a card is recognisable by in a rendered frame:
// the compact strip and the mini cards both print exactly this, so it is read off a
// mini card with its brackets and padding trimmed.
func cardGlyphs(c deck.Card) string {
	return strings.Trim(tuitest.StripANSI(components.RenderMiniCard(styles.NewTheme(true), c)), "[ ]")
}

func containsCard(hand []deck.Card, card deck.Card) bool {
	for _, c := range hand {
		if c.Rank == card.Rank && c.Suit == card.Suit {
			return true
		}
	}
	return false
}

func heroHand(t *testing.T, engine *game.Engine, playerID string) []deck.Card {
	t.Helper()
	_, hand, _ := game.Bind(engine, playerID).Frame(nil)
	return hand
}

func seatedView(t *testing.T, entry Entry, seats, width, height int) tea.Model {
	t.Helper()
	_, m := seatedEngineAndView(t, entry, seats, width, height)
	return m
}

// seatedEngineAndView starts a real table of the given size and binds a view to seat 0.
func seatedEngineAndView(t *testing.T, entry Entry, seats, width, height int) (*game.Engine, tea.Model) {
	t.Helper()

	players := make([]*game.Player, 0, seats)
	for i := range seats {
		players = append(players, &game.Player{
			ID: testutil.SeatID(i + 1), UserID: testutil.UID(i + 1),
			Name: fmt.Sprintf("player%d", i+1),
		})
	}
	rules := entry.Factory()
	engine := game.NewEngine(rules, players, deck.Standard())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	global := router.GlobalContext{
		User:   &db.User{ID: testutil.UID(1), Username: "player1"},
		Theme:  styles.NewTheme(true),
		Width:  width,
		Height: height,
	}
	model := entry.View(global, engine, entry.Slug)
	requireSameGame(t, rules, model)
	return engine, model
}

// requireSameGame is the one check that catches a catalog entry whose Rules and View
// belong to different games. Copy the Hearts entry, change only Rules, and every
// other test still passes: the view renders a table it has no state for, so it prints
// almost nothing and asserts nothing. The packages are named for the game on both
// sides of the tree (internal/game/<g> and internal/tui/views/gameview/<g>), so comparing
// the leaf is enough - and it is the convention a new game has to follow anyway.
func requireSameGame(t *testing.T, rules game.Rules, model tea.Model) {
	t.Helper()
	rulesPkg := path.Base(reflect.TypeOf(rules).Elem().PkgPath())
	viewPkg := path.Base(reflect.TypeOf(model).Elem().PkgPath())

	require.Equal(t, rulesPkg, viewPkg,
		"this catalog entry pairs the %s rules with the %s view", rulesPkg, viewPkg)
}

// setHand overwrites the hand the view has cached, which is the only way to render a
// size the rules never deal. Poker holds its hole cards in its seat rows rather than
// the shared base state, so it keeps the hand it was dealt. The views' model types are
// unexported, so the embedded Session's Base is reached by name.
func setHand(m tea.Model, hand []deck.Card) {
	if path.Base(reflect.TypeOf(m).Elem().PkgPath()) == "poker" {
		return
	}
	reflect.ValueOf(m).Elem().FieldByName("Base").FieldByName("Hand").Set(reflect.ValueOf(hand))
}

// longHand is n distinct cards, cycling suits so the hand is as wide as a real one.
func longHand(n int) []deck.Card {
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	hand := make([]deck.Card, 0, n)
	for i := range n {
		hand = append(hand, deck.Card{
			Rank: deck.AllRanks[i%len(deck.AllRanks)],
			Suit: suits[i%len(suits)],
		})
	}
	return hand
}

func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case !inEscape:
			out.WriteRune(r)
		}
	}
	return out.String()
}
