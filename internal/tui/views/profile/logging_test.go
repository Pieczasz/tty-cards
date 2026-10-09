package profile

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failed queries are the server's fault, so they are ERRORs, logged under the
// session's trace so they can be found from the session that hit them.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestDatabaseErrors_AreLoggedUnderTheSessionTrace(t *testing.T) {
	logs := tuitest.CaptureLogs(t)
	traced := tuitest.TracedContext(t)

	global := router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 40, SessionCtx: traced}
	m, ok := New(global).(*model)
	require.True(t, ok)
	m.Update(profileLoadedMsg{err: errQuery, historyErr: errQuery})
	m.accountDeleted(accountDeletedMsg{err: errQuery})

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 3, "the profile, the history and the delete")
	for _, r := range errs {
		assert.True(t, r.SameTrace(traced), "%q carries the session trace", r.Message)
	}
}

// A missing account is not the database failing, so it stays out of ERROR.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestUserNotFound_IsLoggedAtInfo(t *testing.T) {
	logs := tuitest.CaptureLogs(t)

	global := router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 40}
	m, ok := New(global).(*model)
	require.True(t, ok)
	m.Update(profileLoadedMsg{err: fmt.Errorf("wrapped: %w", db.ErrUserNotFound)})
	m.accountDeleted(accountDeletedMsg{err: db.ErrUserNotFound})

	assert.Empty(t, logs.AtLeast(slog.LevelWarn))
	assert.Len(t, logs.AtLeast(slog.LevelInfo), 2, "the profile and the delete")
}
