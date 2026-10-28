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

// RankByScore is three games' Standings, and its places go straight into Elo: an
// equal place is a draw. Stability is the contract too - equal scores keep seat order,
// which is what makes the order deterministic.
func TestRankByScore(t *testing.T) {
	t.Parallel()

	ids := func(in []Standing) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			out = append(out, s.Player.ID)
		}
		return out
	}
	table := func(seated, left string) *State {
		s := &State{}
		for _, id := range seated {
			s.Players = append(s.Players, &Player{ID: string(id)})
		}
		for _, id := range left {
			s.LeftPlayers = append(s.LeftPlayers, &Player{ID: string(id)})
		}
		return s
	}
	scores := map[string]int{"a": 3, "b": 1, "c": 2}
	byScore := func(p *Player) int { return scores[p.ID] }
	tied := func(*Player) int { return 0 }

	t.Run("ascending by score", func(t *testing.T) {
		t.Parallel()
		got := RankByScore(table("abc", ""), byScore)
		assert.Equal(t, []string{"b", "c", "a"}, ids(got))
		assert.Equal(t, []int{1, 2, 3}, places(got))
	})

	t.Run("a negated score ranks descending", func(t *testing.T) {
		t.Parallel()
		got := RankByScore(table("abc", ""), func(p *Player) int { return -scores[p.ID] })
		assert.Equal(t, []string{"a", "c", "b"}, ids(got))
	})

	t.Run("a tie shares a place and keeps seat order", func(t *testing.T) {
		t.Parallel()
		got := RankByScore(table("abc", ""), tied)
		assert.Equal(t, []string{"a", "b", "c"}, ids(got))
		assert.Equal(t, []int{1, 1, 1}, places(got))
	})

	t.Run("the caller's seats are left alone", func(t *testing.T) {
		t.Parallel()
		state := table("abc", "")
		RankByScore(state, byScore)
		assert.Equal(t, "a", state.Players[0].ID, "ranking must not reorder the seats at the table")
	})

	// Hearts reaches this on any hand-1 disconnect, where every score is still zero:
	// a leaver sharing a seated player's place is a rage-quit recorded as a draw.
	t.Run("a leaver never ties a seated player", func(t *testing.T) {
		t.Parallel()
		got := RankByScore(table("ab", "c"), tied)
		assert.Equal(t, []string{"a", "b", "c"}, ids(got))
		assert.Equal(t, []int{1, 1, 3}, places(got), "the quitter places last on their own")
	})

	// Splitting two leavers into consecutive places moves Elo between people who
	// both walked out; the last to leave ranks first among them.
	t.Run("leavers with an equal score share a place", func(t *testing.T) {
		t.Parallel()
		got := RankByScore(table("ab", "cd"), tied)
		assert.Equal(t, []string{"a", "b", "d", "c"}, ids(got))
		assert.Equal(t, []int{1, 1, 3, 3}, places(got))
	})
}
