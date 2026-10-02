package leaderboard

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed query is the server's fault, so it is an ERROR, and it is logged under the
// session's trace so it can be found from the session that hit it.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestUpdate_AFailedQueryIsLoggedUnderTheSessionTrace(t *testing.T) {
	logs := tuitest.CaptureLogs(t)
	traced := tuitest.TracedContext(t)
	m := &model{
		global:  router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 40, SessionCtx: traced},
		filters: []boardFilter{{label: filterAll}},
	}

	m.Update(loadedMsg{err: errors.New("query failed")})

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 1)
	assert.True(t, errs[0].SameTrace(traced))
}
