package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// captureLogs is a gormLogger writing JSON records into a buffer.
func captureLogs(t *testing.T, verbose bool) (gormLogger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := newGormLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), verbose)
	return l, func() []map[string]any {
		var out []map[string]any
		dec := json.NewDecoder(&buf)
		for dec.More() {
			var rec map[string]any
			require.NoError(t, dec.Decode(&rec))
			out = append(out, rec)
		}
		return out
	}
}

func sqlOf(query string) func() (string, int64) {
	return func() (string, int64) { return query, 1 }
}

// GORM's logger wrote every failed query at ERROR, context.Canceled from a closed
// session included, and the caller then logged and span-recorded the same failure:
// one database error was three records. The caller's log is the one kept.
func TestGormLogger_LeavesFailuresToTheCaller(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"a query error":     errors.New("relation does not exist"),
		"a closed session":  context.Canceled,
		"a missed deadline": context.DeadlineExceeded,
		"no row":            gorm.ErrRecordNotFound,
		"a wrapped cancel":  errors.Join(errors.New("query"), context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l, records := captureLogs(t, false)
			l.Trace(t.Context(), time.Now(), sqlOf("SELECT 1"), err)
			l.Trace(t.Context(), time.Now().Add(-time.Second), sqlOf("SELECT 1"), err)
			assert.Empty(t, records())
		})
	}
}

// A slow query that succeeded is the one thing no caller reports.
func TestGormLogger_WarnsOnASlowQueryWithANumericDuration(t *testing.T) {
	t.Parallel()
	l, records := captureLogs(t, false)

	l.Trace(t.Context(), time.Now(), sqlOf("SELECT fast"), nil)
	l.Trace(t.Context(), time.Now().Add(-time.Second), sqlOf("SELECT slow"), nil)

	got := records()
	require.Len(t, got, 1)
	assert.Equal(t, "WARN", got[0]["level"])
	assert.Equal(t, "SELECT slow", got[0]["sql"])
	assert.InDelta(t, 1.0, got[0]["duration_seconds"], 0.5, "the duration must be a number LogQL can compare")
}

func TestGormLogger_VerboseLogsEveryQuery(t *testing.T) {
	t.Parallel()
	l, records := captureLogs(t, true)

	l.Trace(t.Context(), time.Now(), sqlOf("SELECT 1"), nil)
	l.Trace(t.Context(), time.Now(), sqlOf("SELECT 2"), errors.New("boom"))

	got := records()
	require.Len(t, got, 2)
	assert.Equal(t, "INFO", got[0]["level"])
	assert.Equal(t, "boom", got[1]["error"])

	quiet, none := captureLogs(t, true)
	quiet.LogMode(0).Trace(t.Context(), time.Now(), sqlOf("SELECT 3"), nil)
	assert.Empty(t, none(), "LogMode must still turn the per-query log off")
}

// GORM inlines bound values into the logged SQL unless the logger filters them, and
// they include key fingerprints and usernames.
func TestGormLogger_KeepsBoundValuesOutOfTheSQL(t *testing.T) {
	t.Parallel()
	l, _ := captureLogs(t, false)
	sql, params := l.ParamsFilter(t.Context(), "SELECT * FROM public_keys WHERE fingerprint = $1", "SHA256:abc")
	assert.Equal(t, "SELECT * FROM public_keys WHERE fingerprint = $1", sql)
	assert.Empty(t, params)
}
