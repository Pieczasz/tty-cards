package game

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// AnyScoreAtLeast is the shared match-target check for hearts and gin rummy, so a
// boundary that is off by one ends every match a hand early or a hand late.
func TestAnyScoreAtLeast(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scores map[string]int
		target int
		want   bool
	}{
		{name: "nobody is scored yet", scores: nil, target: 100},
		{name: "everybody is short", scores: map[string]int{"a": 99, "b": 40}, target: 100},
		{name: "exactly on the target ends it", scores: map[string]int{"a": 100}, target: 100, want: true},
		{name: "past the target ends it", scores: map[string]int{"a": 40, "b": 101}, target: 100, want: true},
		{name: "a zero target is already reached", scores: map[string]int{"a": 0}, target: 0, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, AnyScoreAtLeast(tt.scores, tt.target))
		})
	}
}

// Standings and StandingScore have to agree or Engine.Standings records a tie the
// rules never meant, so four games share this sort. Stability is the contract: equal
// scores keep seat order, which is what makes the places deterministic.
func TestStandingsByScore(t *testing.T) {
	t.Parallel()

	a := &Player{ID: "a"}
	b := &Player{ID: "b"}
	c := &Player{ID: "c"}
	players := []*Player{a, b, c}
	ids := func(in []*Player) []string {
		out := make([]string, 0, len(in))
		for _, p := range in {
			out = append(out, p.ID)
		}
		return out
	}

	t.Run("ascending by score", func(t *testing.T) {
		t.Parallel()
		scores := map[string]int{"a": 3, "b": 1, "c": 2}
		got := StandingsByScore(players, func(p *Player) int { return scores[p.ID] })
		assert.Equal(t, []string{"b", "c", "a"}, ids(got))
	})

	t.Run("a negated score sorts descending", func(t *testing.T) {
		t.Parallel()
		scores := map[string]int{"a": 3, "b": 1, "c": 2}
		got := StandingsByScore(players, func(p *Player) int { return -scores[p.ID] })
		assert.Equal(t, []string{"a", "c", "b"}, ids(got))
	})

	t.Run("ties keep seat order", func(t *testing.T) {
		t.Parallel()
		got := StandingsByScore(players, func(*Player) int { return 0 })
		assert.Equal(t, []string{"a", "b", "c"}, ids(got))
	})

	t.Run("the caller's slice is left alone", func(t *testing.T) {
		t.Parallel()
		scores := map[string]int{"a": 3, "b": 1, "c": 2}
		StandingsByScore(players, func(p *Player) int { return scores[p.ID] })
		assert.Equal(t, []string{"a", "b", "c"}, ids(players),
			"sorting standings must not reorder the seats at the table")
	})
}
