package game

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Uno seats ten and poker nine, so a layout that runs out of slots leaves players
// off the table. Every opponent has to land in exactly one zone at every table size.
func TestSplitZones_PlacesEveryOpponentExactlyOnce(t *testing.T) {
	t.Parallel()

	for n := range 12 {
		t.Run(fmt.Sprintf("opponents=%d", n), func(t *testing.T) {
			t.Parallel()

			opponents := make([]string, 0, n)
			for i := range n {
				opponents = append(opponents, fmt.Sprintf("p%d", i))
			}

			z := SplitZones(opponents)
			placed := slices.Concat(z.Left, z.Top, z.Right)

			// ElementsMatch, not Len: a seat drawn twice would keep the count right
			// while losing a different player off the table.
			assert.ElementsMatch(t, opponents, placed)
			assert.LessOrEqual(t, len(z.Top), topCapacity, "the top row has to fit across a terminal")
		})
	}
}

// The sides carry the overflow, so neither of them may run away with the table.
func TestSplitZones_BalancesTheSides(t *testing.T) {
	t.Parallel()

	for n := 2; n <= 12; n++ {
		opponents := make([]int, n)
		z := SplitZones(opponents)
		assert.Lenf(t, z.Left, len(z.Right), "%d opponents: the sides have to match", n)
	}
}

func TestSplitZones_EmptyTable(t *testing.T) {
	t.Parallel()

	z := SplitZones([]string{})
	assert.Empty(t, z.Left)
	assert.Empty(t, z.Top)
	assert.Empty(t, z.Right)
}

// The name and the hand count are what a player reads off somebody else's seat, so
// both have to be on screen for every opponent however many are seated.
func TestRenderOpponentEdges_ShowsEverySeat(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	for _, seats := range []int{1, 2, 3, 6, 9} {
		t.Run(fmt.Sprintf("opponents=%d", seats), func(t *testing.T) {
			t.Parallel()

			base := BaseState{Opponents: make([]game.PlayerSnapshot, 0, seats)}
			for i := range seats {
				base.Opponents = append(base.Opponents, game.PlayerSnapshot{
					ID:       fmt.Sprintf("p%d", i),
					Username: fmt.Sprintf("player%d", i),
					HandSize: i + 1,
				})
			}

			left, right := RenderOpponentSides(theme, base, 40, false)
			top := RenderOpponentTop(theme, base, 200, false)
			rendered := tuitest.StripANSI(strings.Join([]string{top, left, right}, "\n"))

			for _, o := range base.Opponents {
				assert.Containsf(t, rendered, o.Username, "%s is not on the table", o.Username)
				assert.Containsf(t, rendered, fmt.Sprintf("[%d cards]", o.HandSize),
					"%s's hand count is missing", o.Username)
			}
		})
	}
}

// The seat on turn is the one a player is waiting on, and it is named by ID:
// two players may share a display name.
func TestRenderOpponentEdges_MarksTheSeatOnTurn(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	base := BaseState{
		CurrentPlayerID: "p1",
		Opponents: []game.PlayerSnapshot{
			{ID: "p0", Username: "same", HandSize: 3},
			{ID: "p1", Username: "same", HandSize: 3},
		},
	}

	turnLeft, _ := RenderOpponentSides(theme, base, 40, false)
	base.CurrentPlayerID = "p0"
	otherLeft, _ := RenderOpponentSides(theme, base, 40, false)

	require.NotEqual(t, turnLeft, otherLeft,
		"the highlight has to follow the player ID, not the name they share")
}

// The side stacks used to emit one row per card with no bound, so an Uno hand of
// twenty-odd cards drew a seat taller than the terminal and pushed the hero's own hand
// off the bottom. The hand count beside the art is what carries the truth once the
// stack is cut short.
func TestRenderOpponentSides_FitTheHeightTheyAreGiven(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	for _, seats := range []int{2, 3, 6} {
		for _, handSize := range []int{1, 7, 25} {
			for _, height := range []int{6, 12, 30} {
				t.Run(fmt.Sprintf("seats=%d/hand=%d/height=%d", seats, handSize, height), func(t *testing.T) {
					t.Parallel()

					base := BaseState{Opponents: make([]game.PlayerSnapshot, 0, seats)}
					for i := range seats {
						base.Opponents = append(base.Opponents, game.PlayerSnapshot{
							ID:       fmt.Sprintf("p%d", i),
							Username: fmt.Sprintf("player%d", i),
							HandSize: handSize,
						})
					}

					left, right := RenderOpponentSides(theme, base, height, false)
					for name, side := range map[string]string{"left": left, "right": right} {
						if side == "" {
							continue
						}
						assert.LessOrEqualf(t, lg.Height(side), height, "the %s stack is taller than its band", name)
					}
				})
			}
		}
	}
}

// A budget too small for even one card is drawn as a name and a count, not as art cut
// down to nothing.
func TestRenderOpponentSeat_FallsBackToNamesWhenTheArtCannotFit(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	base := BaseState{Opponents: []game.PlayerSnapshot{
		{ID: "p0", Username: "player0", HandSize: 5},
		{ID: "p1", Username: "player1", HandSize: 5},
	}}

	left, _ := RenderOpponentSides(theme, base, 4, false)
	rendered := tuitest.StripANSI(left)
	assert.Contains(t, rendered, "player0")
	assert.Contains(t, rendered, "[5 cards]")
	assert.NotContains(t, rendered, "░", "no half-drawn card backs")
}
