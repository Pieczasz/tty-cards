package game_test

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestPlayer_Equal(t *testing.T) {
	t.Parallel()
	p1 := &game.Player{ID: "a", UserID: testutil.UID(1)}
	p2 := &game.Player{ID: "b", UserID: testutil.UID(1)}
	p3 := &game.Player{ID: "a", UserID: testutil.UID(2)}

	t.Run("same account is the same player whatever the session ID", func(t *testing.T) {
		t.Parallel()
		assert.True(t, p1.Equal(p2))
	})

	t.Run("different accounts are different players", func(t *testing.T) {
		t.Parallel()
		assert.False(t, p1.Equal(p3))
	})

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var pNil *game.Player
		assert.False(t, pNil.Equal(p1))
	})

	t.Run("no account falls back to ID", func(t *testing.T) {
		t.Parallel()
		a := &game.Player{ID: "x"}
		b := &game.Player{ID: "x"}
		c := &game.Player{ID: "y"}
		assert.True(t, a.Equal(b))
		assert.False(t, a.Equal(c))
		assert.False(t, (&game.Player{}).Equal(&game.Player{}), "two unidentified players are not the same one")
	})
}

func TestPlayer_DisplayName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "alice", (&game.Player{ID: "1", Name: "alice"}).DisplayName())
	assert.Equal(t, "1", (&game.Player{ID: "1"}).DisplayName(), "a nameless seat shows its ID")
	assert.Empty(t, (*game.Player)(nil).DisplayName())
}
