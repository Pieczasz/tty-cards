package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/testsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// logSink records every slog record, Debug included, while a test runs. It swaps
// the process-global default, so the tests using it do not run in parallel.
type logSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *logSink) WithAttrs([]slog.Attr) slog.Handler       { return s }
func (s *logSink) WithGroup(string) slog.Handler            { return s }

func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}

// levels is how many times msg was logged at each level.
func (s *logSink) levels(msg string) map[slog.Level]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := map[slog.Level]int{}
	for _, r := range s.records {
		if r.Message == msg {
			got[r.Level]++
		}
	}
	return got
}

// snapshot is every record so far.
func (s *logSink) snapshot() []slog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]slog.Record(nil), s.records...)
}

func captureLogs(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

// An ssh-agent offers every key it holds, and each offer is one call to the auth
// handler: counted per call, one throttled client read as a burst of refusals. A
// refusal at auth is not a session either, so it stays out of ttycards.ssh.sessions.
//
//nolint:paralleltest // reads process-global metrics
func TestRateLimitAuth_CountsARefusedConnectionOnce(t *testing.T) {
	const rejects = "ttycards.ssh.auth.rejects"
	before := metricTotal(t, rejects, "reason", "ratelimit")
	sessionsBefore := metricTotal(t, "ttycards.ssh.sessions", "outcome", "rejected_ratelimit")
	unkeyableBefore := metricTotal(t, rejects, "reason", "unkeyable")

	handler := rateLimitAuth(ratelimit.New(1, time.Minute), ratelimit.New(100, time.Minute), ratelimit.New(1, time.Minute),
		func(ssh.Context, ssh.PublicKey) bool { return true })
	agent := newStubSSHContext(stubAddr{"198.51.100.20:2222"})
	for range 6 {
		handler(agent, nil)
	}
	handler(newStubSSHContext(stubAddr{"198.51.100.20:2223"}), nil)

	assert.Equal(t, int64(2), metricTotal(t, rejects, "reason", "ratelimit")-before,
		"two refused connections, however many keys they offered")
	assert.Equal(t, sessionsBefore, metricTotal(t, "ttycards.ssh.sessions", "outcome", "rejected_ratelimit"),
		"an auth refusal was counted as a session")

	unkeyable := newStubSSHContext(stubAddr{"no-port"})
	handler(unkeyable, nil)
	handler(unkeyable, nil)
	assert.Equal(t, int64(1), metricTotal(t, rejects, "reason", "unkeyable")-unkeyableBefore)
}

// A flood of throttled attempts used to write one warn line each, with the address,
// into a store kept for 14 days. One per /48 per window is enough to see it.
//
//nolint:paralleltest // swaps the process-global logger
func TestRateLimitAuth_WarnsOncePerNetworkPerWindow(t *testing.T) {
	logs := captureLogs(t)
	handler := rateLimitAuth(ratelimit.New(1, time.Minute), ratelimit.New(100, time.Minute), ratelimit.New(1, time.Minute),
		func(ssh.Context, ssh.PublicKey) bool { return true })

	// Ten /64s of one /48, each spending its budget and then refused.
	for _, net := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "a"} {
		addr := stubAddr{"[2001:db8:7:" + net + "::1]:22"}
		require.True(t, handler(newStubSSHContext(addr), nil))
		require.False(t, handler(newStubSSHContext(addr), nil))
	}

	got := logs.levels("rate limited ssh connection")
	assert.Equal(t, 1, got[slog.LevelWarn], "one warn per /48 per window")
	assert.Equal(t, 9, got[slog.LevelDebug], "the rest are still there at debug")
}

// A taken name or an already-registered key is the player's situation, not the
// server's failure: logged at error it paged on every typo.
//
//nolint:paralleltest // swaps the process-global logger
func TestLoadOrRegisterUser_LogsUserConditionsBelowError(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		want  slog.Level
	}{
		{name: "a taken name", cause: db.ErrUsernameTaken, want: slog.LevelInfo},
		{name: "a key registered already", cause: db.ErrKeyAlreadyRegistered, want: slog.LevelInfo},
		{name: "a database failure", cause: errors.New("disk on fire"), want: slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			repo := new(MockUserRepository)
			repo.On("LoadUserByFingerprint", mock.Anything, "fp").Return(nil, nil, nil)
			repo.On("RegisterUserWithSSHKey", mock.Anything, "user", "fp").Return(nil, nil, tt.cause)

			_, err := LoadOrRegisterUser(t.Context(), repo, "user", "fp", nil)
			require.Error(t, err)

			assert.Equal(t, map[slog.Level]int{tt.want: 1}, logs.levels("failed to register new user"))
		})
	}
}

// The duration histogram is labelled with how a session ended. Every refusal used to
// land in it as "normal", so a burst of full-server refusals read as a wave of very
// short, perfectly healthy sessions.
//
//nolint:paralleltest // reads process-global metrics
func TestFinishSession_RecordsHowTheSessionEnded(t *testing.T) {
	const durations = "ttycards.ssh.session.duration"
	tripped := newInputGuard(nil, nil)
	tripped.tripped.Store(true)

	tests := []struct {
		name  string
		setup func(reg *sessionRegistry, s *stubSession, st *sessionState)
		want  string
	}{
		{
			name: "refused, server full",
			setup: func(reg *sessionRegistry, s *stubSession, _ *sessionState) {
				reg.failSessionf(s, "rejected_full", errServerFull, "full\n")
			},
			want: "rejected_full",
		},
		{
			name: "refused at sign-in",
			setup: func(reg *sessionRegistry, s *stubSession, _ *sessionState) {
				reg.failSessionf(s, "auth_failed", errNoPublicKey, "no key\n")
			},
			want: "auth_failed",
		},
		{name: "cut off for flooding", setup: func(_ *sessionRegistry, _ *stubSession, st *sessionState) { st.input = tripped }, want: "input_limit"},
		{name: "a panic", setup: func(_ *sessionRegistry, _ *stubSession, st *sessionState) { st.panicked = true }, want: "panic"},
		{name: "a player who left", setup: func(*sessionRegistry, *stubSession, *sessionState) {}, want: "normal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := metricTotal(t, durations, "outcome", tt.want)
			normal := metricTotal(t, durations, "outcome", "normal")
			s := &stubSession{addr: stubAddr{"10.0.0.30:1"}}
			_, span := noop.NewTracerProvider().Tracer("test").Start(t.Context(), "ssh.session")
			st := &sessionState{traceCtx: t.Context(), span: span, started: time.Now()}
			reg := &sessionRegistry{}
			reg.store(s, st)

			tt.setup(reg, s, st)
			reg.finishSession(s, st)

			assert.Equal(t, int64(1), metricTotal(t, durations, "outcome", tt.want)-before)
			if tt.want != "normal" {
				assert.Equal(t, normal, metricTotal(t, durations, "outcome", "normal"), "recorded as normal")
			}
		})
	}
}

// A session refused because the server is draining returned before the session span
// and the counters, so a shutdown that turned players away left no count of them.
//
//nolint:paralleltest // reads process-global metrics
func TestSessionLifecycle_ARefusalWhileDrainingIsCounted(t *testing.T) {
	before := metricTotal(t, "ttycards.ssh.sessions", "outcome", "rejected_draining")
	reg := &sessionRegistry{}
	require.NoError(t, (&Server{sessions: reg}).WaitSessions(t.Context()))

	srv := &ssh.Server{Handler: sessionLifecycle(Deps{}, reg)(func(ssh.Session) {})}
	_, _ = testsession.New(t, srv, nil).CombinedOutput("")

	assert.Equal(t, int64(1), metricTotal(t, "ttycards.ssh.sessions", "outcome", "rejected_draining")-before)
}

// A program's error is logged once, through slog: not when the input guard or
// reportPanic has already said why the session ended, and not when a newer session
// displacing it cancelled the program's context.
//
//nolint:paralleltest // swaps the slog default
func TestReportProgramExit(t *testing.T) {
	const msg = "session program ended with an error"
	tests := []struct {
		name     string
		err      error
		panicked bool
		ended    bool
		want     map[slog.Level]int
	}{
		{name: "a displacement", err: fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.Canceled), ended: true, want: map[slog.Level]int{}},
		{name: "an input trip", err: fmt.Errorf("%w: %w", tea.ErrProgramKilled, errInputOverBudget), want: map[slog.Level]int{}},
		{name: "a reported panic", err: tea.ErrProgramPanic, panicked: true, want: map[slog.Level]int{}},
		{name: "anything else", err: tea.ErrProgramKilled, want: map[slog.Level]int{slog.LevelError: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			s := &stubSession{addr: stubAddr{"10.0.0.31:1"}}
			reg := &sessionRegistry{}
			st := &sessionState{traceCtx: t.Context(), panicked: tt.panicked}
			st.run, st.endRun = context.WithCancel(t.Context())
			if tt.ended {
				st.endRun()
			}
			reg.store(s, st)

			reg.reportProgramExit(s, tt.err)

			assert.Equal(t, tt.want, logs.levels(msg))
		})
	}
}
