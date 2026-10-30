package profile

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	"uuid"

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
	m.Update(deleteAccount(traced, failingDelete(errQuery), testutil.UID(1))())

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 3, "the profile, the history and the delete, each once")
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
	m.Update(deleteAccount(t.Context(), failingDelete(db.ErrUserNotFound), testutil.UID(1))())

	assert.Empty(t, logs.AtLeast(slog.LevelWarn))
	assert.Len(t, logs.AtLeast(slog.LevelInfo), 2, "the profile and the delete")
}

// The player can close the terminal while the erasure runs, and then its result
// message reaches a program that has already quit. The command logs the failure
// itself so it is not lost with the message.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestDeleteAccount_LogsAFailureWithoutTheView(t *testing.T) {
	logs := tuitest.CaptureLogs(t)
	traced := tuitest.TracedContext(t)
	session, endSession := context.WithCancel(traced)
	cmd := deleteAccount(session, failingDelete(errQuery), testutil.UID(1))
	endSession()

	cmd()

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 1)
	assert.True(t, errs[0].SameTrace(traced), "the failure keeps the session trace")
}

func failingDelete(err error) fakeUsers {
	return fakeUsers{deleteAccount: func(context.Context, uuid.UUID) error { return err }}
}
