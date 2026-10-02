package ginrummy

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/require"
)

// extra is the gin rummy state of a table, failing the test on anything else.
func extra(t *testing.T, s *game.State) *State {
	t.Helper()
	e, ok := s.Extra.(*State)
	require.True(t, ok, "state.Extra is %T, not *ginrummy.State", s.Extra)
	return e
}
