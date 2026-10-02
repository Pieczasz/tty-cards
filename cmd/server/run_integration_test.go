//go:build integration

package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/require"
)

// The whole composition root against a real database: it has to come up healthy,
// answer the container healthcheck, and drain cleanly on SIGTERM - and a boot that
// cannot finish wiring has to release what it already opened.
//
//nolint:paralleltest // run installs the process-wide slog default and otel providers
func TestRun_AgainstPostgres(t *testing.T) {
	dsn, err := url.Parse(testutil.PostgresDSN(t))
	require.NoError(t, err)
	password, _ := dsn.User.Password()
	databaseEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("DB_HOST", dsn.Hostname())
		t.Setenv("DB_PORT", dsn.Port())
		t.Setenv("DB_USER", dsn.User.Username())
		t.Setenv("DB_PASSWORD", password)
		t.Setenv("DB_NAME", strings.TrimPrefix(dsn.Path, "/"))
		t.Setenv("DB_SSLMODE", "disable")
	}

	t.Run("serves until signalled", func(t *testing.T) {
		serverEnv(t, fakeCollector(t))
		databaseEnv(t)
		captureLogs(t)

		exited := make(chan error, 1)
		go func() { exited <- run() }()

		awaitHealthy(t, exited)
		// The real signal, not a channel send: run has it registered by now, so the
		// test binary is not what it terminates.
		self, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		require.NoError(t, self.Signal(syscall.SIGTERM))

		select {
		case err := <-exited:
			require.NoError(t, err, "a requested shutdown is not a failure")
		case <-time.After(30 * time.Second):
			t.Fatal("run ignored SIGTERM")
		}
	})

	t.Run("an unwritable host key stops the boot", func(t *testing.T) {
		serverEnv(t, fakeCollector(t))
		databaseEnv(t)
		captureLogs(t)
		notADir := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(notADir, nil, 0o600))
		t.Setenv("SSH_KEY_PATH", filepath.Join(notADir, "host_key"))

		require.ErrorContains(t, run(), "setup ssh server")
	})
}

// awaitHealthy polls the same healthcheck the container runs until it passes, failing
// fast if run gives up first.
func awaitHealthy(t *testing.T, exited <-chan error) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for healthcheck() != 0 {
		select {
		case err := <-exited:
			t.Fatalf("run returned before it was healthy: %v", err)
		case <-deadline:
			t.Fatal("the server never became healthy")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
