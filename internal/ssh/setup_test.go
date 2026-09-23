package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/ratelimit"

	"charm.land/ssh"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

func TestSetupServer_Errors(t *testing.T) {
	t.Parallel()

	deps := ServerDependencies{
		Config: &config.Config{SSHKeyPath: "/invalid/path/that/doesnt/exist"},
	}
	_, err := SetupServer(deps)
	assert.ErrorContains(t, err, "error while saving keypair")
}

func TestSetupServer_SetsConnectionTimeouts(t *testing.T) {
	t.Parallel()

	deps := ServerDependencies{
		Config: &config.Config{
			SSHKeyPath:      t.TempDir() + "/id_ed25519",
			RateLimitCount:  5,
			RateLimitWindow: time.Second,
		},
	}

	server, err := SetupServer(deps)
	require.NoError(t, err)

	assert.Equal(t, 20*time.Second, server.HandshakeTimeout, "an unauthenticated connection must be dropped")
	assert.Equal(t, 30*time.Minute, server.IdleTimeout, "a connection that vanished without a FIN must be reaped")
}

// A host key any other account on the box can read lets them impersonate the server.
func TestEnsureHostKeyPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    os.FileMode
		wantErr string
	}{
		{name: "already private", mode: 0o600},
		{name: "group readable is tightened", mode: 0o640},
		{name: "world readable is tightened", mode: 0o644},
		{name: "world writable is tightened", mode: 0o666},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "id_ed25519")
			require.NoError(t, os.WriteFile(path, []byte("key"), tt.mode))

			require.NoError(t, ensureHostKeyPermissions(path))

			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Zero(t, info.Mode().Perm()&0o077,
				"the host key is still readable by someone other than the server")
		})
	}

	t.Run("a missing key is an error, not a silent pass", func(t *testing.T) {
		t.Parallel()
		err := ensureHostKeyPermissions(filepath.Join(t.TempDir(), "absent"))
		assert.ErrorContains(t, err, "stat ssh host key")
	})
}

// stubAddr is an address whose String() is whatever the test needs, including forms
// net.SplitHostPort refuses.
type stubAddr struct{ s string }

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return a.s }

// stubSSHContext is enough ssh.Context for the auth handlers: they only read the
// remote address and the session id, and log against the context.
type stubSSHContext struct {
	ssh.Context
	ctx  context.Context
	addr net.Addr
}

func (c *stubSSHContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *stubSSHContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c *stubSSHContext) Err() error                  { return c.ctx.Err() }
func (c *stubSSHContext) Value(key any) any           { return c.ctx.Value(key) }
func (c *stubSSHContext) RemoteAddr() net.Addr        { return c.addr }
func (c *stubSSHContext) SessionID() string           { return "stub-session" }

func newStubSSHContext(addr net.Addr) *stubSSHContext {
	return &stubSSHContext{ctx: context.Background(), addr: addr}
}

// An unkeyable address must be refused, not admitted: "host:port" as a bucket key
// gives every attempt its own window and silently turns the limiter off.
func TestRateLimitAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		addr      net.Addr
		attempts  int
		wantAllow bool
		// wantNextCalls is how often the wrapped handler ran: the limiter's job is to
		// stop reaching it, and an unkeyable address must never reach it at all.
		wantNextCalls int
	}{
		{
			name: "inside the budget", addr: stubAddr{"10.0.0.1:2222"},
			attempts: 1, wantAllow: true, wantNextCalls: 1,
		},
		{
			name: "over the budget", addr: stubAddr{"10.0.0.2:2222"},
			attempts: 4, wantAllow: false, wantNextCalls: 3,
		},
		{name: "no address at all", addr: nil, attempts: 1, wantAllow: false},
		{name: "address without a port", addr: stubAddr{"10.0.0.3"}, attempts: 1, wantAllow: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nextCalls := 0
			handler := rateLimitAuth(
				ratelimit.NewSlidingWindowLimiter(3, time.Minute),
				func(ssh.Context, ssh.PublicKey) bool { nextCalls++; return true },
			)

			var allowed bool
			for range tt.attempts {
				allowed = handler(newStubSSHContext(tt.addr), nil)
			}
			assert.Equal(t, tt.wantAllow, allowed)
			assert.Equal(t, tt.wantNextCalls, nextCalls, "the wrapped handler ran when it should not have")
		})
	}
}

// The two IPv6 addresses share a /64, so they share a bucket: keying on the full
// address is meaningless when one customer is handed 2^64 of them.
func TestRateLimitAuth_CollapsesIPv6ToItsPrefix(t *testing.T) {
	t.Parallel()

	handler := rateLimitAuth(
		ratelimit.NewSlidingWindowLimiter(1, time.Minute),
		func(ssh.Context, ssh.PublicKey) bool { return true },
	)

	assert.True(t, handler(newStubSSHContext(stubAddr{"[2001:db8::1]:2222"}), nil))
	assert.False(t, handler(newStubSSHContext(stubAddr{"[2001:db8::dead:beef]:2222"}), nil),
		"a second address in the same /64 spent someone else's budget")
}

func TestAllowRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limiter  *ratelimit.SlidingWindowLimiter
		addr     net.Addr
		attempts int
		want     bool
	}{
		{name: "no limiter configured", limiter: nil, addr: stubAddr{"10.0.0.1:1"}, attempts: 1, want: true},
		{
			name:    "inside the budget",
			limiter: ratelimit.NewSlidingWindowLimiter(2, time.Hour),
			addr:    stubAddr{"10.0.0.2:1"}, attempts: 2, want: true,
		},
		{
			name:    "over the budget",
			limiter: ratelimit.NewSlidingWindowLimiter(2, time.Hour),
			addr:    stubAddr{"10.0.0.3:1"}, attempts: 3, want: false,
		},
		{
			name:    "an unkeyable address fails closed",
			limiter: ratelimit.NewSlidingWindowLimiter(2, time.Hour),
			addr:    stubAddr{"not-an-address"}, attempts: 1, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &stubSession{addr: tt.addr}

			var got bool
			for range tt.attempts {
				got = allowRegistration(context.Background(), tt.limiter, s)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// stubSession is enough ssh.Session for sessionModel's refusal paths: wish.Fatalf
// writes to Stderr, then exits and closes.
type stubSession struct {
	ssh.Session
	pubKey ssh.PublicKey
	user   string
	addr   net.Addr
	errOut bytes.Buffer
	exited int
	closed bool
}

func (s *stubSession) PublicKey() ssh.PublicKey { return s.pubKey }
func (s *stubSession) User() string             { return s.user }
func (s *stubSession) RemoteAddr() net.Addr     { return s.addr }
func (s *stubSession) Stderr() io.ReadWriter    { return &s.errOut }
func (s *stubSession) Exit(code int) error      { s.exited = code; return nil }
func (s *stubSession) Close() error             { s.closed = true; return nil }
func (s *stubSession) Context() ssh.Context     { return newStubSSHContext(s.addr) }

// stubUserRepo answers only what sessionModel asks of it.
type stubUserRepo struct {
	db.UserRepository
	user *db.User
	err  error
}

func (r stubUserRepo) LoadUserByFingerprint(context.Context, string) (*db.User, *db.PublicKey, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.user, &db.PublicKey{}, nil
}

func (stubUserRepo) UpdateUserActivity(context.Context, *db.User, *db.PublicKey) error { return nil }

func newSessionDeps(repo db.UserRepository) ServerDependencies {
	return ServerDependencies{
		Config:         &config.Config{},
		UserRepository: repo,
		LobbyManager:   lobby.NewManager(context.Background(), nil),
		GameRegistry:   game.NewRegistry(),
	}
}

// Every refusal has to end the session with a message rather than hand bubbletea a
// nil model and hope, and none of them may leave a tracker slot claimed: the slot is
// per account, so a stranded one locks that player out until the process restarts.
func TestSessionModel_RefusalPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		session    *stubSession
		repo       db.UserRepository
		tracker    *SessionTracker
		storeState bool
		wantOutput string
	}{
		{
			name:       "no public key",
			session:    &stubSession{addr: stubAddr{"10.0.0.1:1"}},
			repo:       stubUserRepo{user: &db.User{ID: testutil.UID(1), Username: "anyone"}},
			tracker:    NewSessionTracker(0),
			storeState: true,
			wantOutput: "SSH key authentication is required",
		},
		{
			name:       "the database is down",
			session:    &stubSession{addr: stubAddr{"10.0.0.2:1"}, pubKey: testPublicKey(t)},
			repo:       stubUserRepo{err: errors.New("connection refused")},
			tracker:    NewSessionTracker(0),
			storeState: true,
			wantOutput: "internal server error",
		},
		{
			name:       "the server is full",
			session:    &stubSession{addr: stubAddr{"10.0.0.3:1"}, pubKey: testPublicKey(t)},
			repo:       stubUserRepo{user: &db.User{ID: testutil.UID(2), Username: "late"}},
			tracker:    fullTracker(t),
			storeState: true,
			wantOutput: "The server is full",
		},
		{
			name:       "the session state was already torn down",
			session:    &stubSession{addr: stubAddr{"10.0.0.4:1"}, pubKey: testPublicKey(t)},
			repo:       stubUserRepo{user: &db.User{ID: testutil.UID(3), Username: "ghost"}},
			tracker:    NewSessionTracker(0),
			storeState: false,
			wantOutput: "could not be started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.storeState {
				sessionStates.Store(tt.session, &sessionState{traceCtx: context.Background()})
				t.Cleanup(func() { sessionStates.Delete(tt.session) })
			}
			before := tt.tracker.Count()

			deps := newSessionDeps(tt.repo)
			limiter := ratelimit.NewSlidingWindowLimiter(5, time.Hour)
			model, opts := sessionModel(deps, tt.tracker, limiter)(tt.session)

			assert.Nil(t, model, "a refused session must not be handed to bubbletea")
			assert.Nil(t, opts)
			assert.Contains(t, tt.session.errOut.String(), tt.wantOutput)
			assert.Equal(t, 1, tt.session.exited, "the client is left waiting on a session that never starts")
			assert.Equal(t, before, tt.tracker.Count(), "a refused session kept a tracker slot")
		})
	}
}

// testPublicKey is any valid key: nothing under test inspects it beyond fingerprinting.
func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	return key
}

// fullTracker is a one-slot tracker with its slot already taken by another account.
func fullTracker(t *testing.T) *SessionTracker {
	t.Helper()
	tracker := NewSessionTracker(1)
	_, err := tracker.Connect(testutil.UID(999), nil)
	require.NoError(t, err)
	return tracker
}

// The accepted path is where the tracker slot, the lobby seat and the view all get
// tied to one session; every teardown defer reads them back out of sessionState.
func TestSessionModel_AcceptedSessionIsFullyRegistered(t *testing.T) {
	t.Parallel()

	user := &db.User{ID: testutil.UID(42), Username: "player"}
	s := &stubSession{addr: stubAddr{"10.0.0.9:1"}, pubKey: testPublicKey(t), user: "player"}
	st := &sessionState{traceCtx: context.Background()}
	sessionStates.Store(s, st)
	t.Cleanup(func() { sessionStates.Delete(s) })

	tracker := NewSessionTracker(0)
	deps := newSessionDeps(stubUserRepo{user: user})
	limiter := ratelimit.NewSlidingWindowLimiter(5, time.Hour)

	model, _ := sessionModel(deps, tracker, limiter)(s)
	require.NotNil(t, model)
	t.Cleanup(func() { st.model.Close() })

	assert.True(t, st.owns, "teardown skips a session that does not own its slot")
	assert.Equal(t, user, st.user)
	assert.NotZero(t, st.gen)
	assert.NotNil(t, st.model, "closeSessionModel would leave the view's subscriptions running")
	assert.True(t, tracker.Owns(user.ID, st.gen))
	assert.Zero(t, s.exited, "an accepted session must not be hung up on")
}

// sessionProgram is what wish calls; handing it a refused session must produce no
// program rather than a bubbletea run over a nil model.
func TestSessionProgram_RefusedSessionGetsNoProgram(t *testing.T) {
	t.Parallel()

	s := &stubSession{addr: stubAddr{"10.0.0.10:1"}}
	sessionStates.Store(s, &sessionState{traceCtx: context.Background()})
	t.Cleanup(func() { sessionStates.Delete(s) })

	deps := newSessionDeps(stubUserRepo{user: &db.User{ID: testutil.UID(5)}})
	program := sessionProgram(deps, NewSessionTracker(0),
		ratelimit.NewSlidingWindowLimiter(5, time.Hour))

	assert.Nil(t, program(s), "a refused session must not get a bubbletea program")
}

// bubbletea's own recover writes the stack to the *server's* stderr and ends the
// program, so recoverSession never fires and the client is left staring at a frozen
// screen that then disconnects. The notice is the only thing it ever sees.
func TestReportingModel_TellsTheClientAboutThePanic(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"init", "update", "view"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			s := &stubSession{addr: stubAddr{"10.0.0.11:1"}}
			sessionStates.Store(s, &sessionState{traceCtx: context.Background()})
			t.Cleanup(func() { sessionStates.Delete(s) })

			m := reportingModel{Model: panicModel{on: method}, session: s}
			require.Panics(t, func() {
				switch method {
				case "init":
					m.Init()
				case "update":
					m.Update(nil)
				case "view":
					m.View()
				}
			})

			assert.Contains(t, s.errOut.String(), "An unexpected internal error occurred",
				"the panic reached the logs and the span but never the player")
		})
	}
}
