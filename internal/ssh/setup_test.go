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

	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"charm.land/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	gossh "golang.org/x/crypto/ssh"
)

func TestNewServer_Errors(t *testing.T) {
	t.Parallel()

	deps := Deps{
		Config:  &config.Config{SSHKeyPath: "/invalid/path/that/doesnt/exist"},
		Tracker: NewSessionTracker(0),
	}
	_, err := NewServer(deps)
	assert.ErrorContains(t, err, "write host key")
}

// The tracker is shared with the stats API, which counts who is online from it. A
// server that quietly built its own would leave that count at zero forever.
func TestNewServer_RequiresATracker(t *testing.T) {
	t.Parallel()

	_, err := NewServer(Deps{
		Config: &config.Config{SSHKeyPath: t.TempDir() + "/id_ed25519", RateLimitCount: 5, RateLimitWindow: time.Second},
	})
	require.ErrorIs(t, err, errNoTracker)
}

func TestNewServer_SetsConnectionTimeouts(t *testing.T) {
	t.Parallel()

	deps := Deps{
		Config: &config.Config{
			SSHKeyPath:      t.TempDir() + "/id_ed25519",
			RateLimitCount:  5,
			RateLimitWindow: time.Second,
		},
		Tracker: NewSessionTracker(0),
	}

	server, err := NewServer(deps)
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

// stubSSHContext is enough ssh.Context for the auth handlers and the teardown: they
// read the remote address, session id and client version, keep per-connection values
// and log against the context.
type stubSSHContext struct {
	ssh.Context
	ctx  context.Context
	addr net.Addr
}

func (c *stubSSHContext) SetValue(key, value any) { c.ctx = context.WithValue(c.ctx, key, value) }
func (c *stubSSHContext) ClientVersion() string   { return "SSH-2.0-stub" }

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
				ratelimit.New(3, time.Minute), ratelimit.New(1, time.Minute),
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
		ratelimit.New(1, time.Minute), ratelimit.New(1, time.Minute),
		func(ssh.Context, ssh.PublicKey) bool { return true },
	)

	assert.True(t, handler(newStubSSHContext(stubAddr{"[2001:db8::1]:2222"}), nil))
	assert.False(t, handler(newStubSSHContext(stubAddr{"[2001:db8::dead:beef]:2222"}), nil),
		"a second address in the same /64 spent someone else's budget")
}

func testBudget(perNet, perWide, global int) registrationBudget {
	return registrationBudget{
		net:    ratelimit.New(perNet, time.Hour),
		wide:   ratelimit.New(perWide, time.Hour),
		global: ratelimit.New(global, time.Hour),
	}
}

func TestAllowRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		budget registrationBudget
		addrs  []string
		want   bool
	}{
		{
			name: "inside the budget", budget: testBudget(2, 10, 10),
			addrs: []string{"10.0.0.2:1", "10.0.0.2:1"}, want: true,
		},
		{
			name: "over the network's budget", budget: testBudget(2, 10, 10),
			addrs: []string{"10.0.0.3:1", "10.0.0.3:1", "10.0.0.3:1"}, want: false,
		},
		{
			// A free tunnel is a /48: without the wide budget each of its 65,536 /64s
			// would get a fresh one.
			name: "a /48 shares one budget across its /64s", budget: testBudget(2, 3, 10),
			addrs: []string{"[2001:db8:1:1::1]:1", "[2001:db8:1:2::1]:1", "[2001:db8:1:3::1]:1", "[2001:db8:1:4::1]:1"},
			want:  false,
		},
		{
			name: "the next /48 is somebody else", budget: testBudget(2, 1, 10),
			addrs: []string{"[2001:db8:1::1]:1", "[2001:db8:2::1]:1"}, want: true,
		},
		{
			name: "the server-wide ceiling holds across networks", budget: testBudget(2, 10, 3),
			addrs: []string{"10.0.1.1:1", "10.0.1.2:1", "10.0.1.3:1", "10.0.1.4:1"}, want: false,
		},
		{
			name: "an unkeyable address fails closed", budget: testBudget(2, 10, 10),
			addrs: []string{"not-an-address"}, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got bool
			for _, addr := range tt.addrs {
				got = allowRegistration(t.Context(), tt.budget, &stubSession{addr: stubAddr{addr}})
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// A registration refused by a wider budget did not happen, so it must not spend the
// narrower ones: under a flood that fills the server-wide ceiling, a real player who
// retries would otherwise find their own network locked out for the hour after.
func TestAllowRegistration_ARefusalSpendsNothing(t *testing.T) {
	t.Parallel()
	budget := testBudget(1, 1, 5)
	first := &stubSession{addr: stubAddr{"[2001:db8:1:1::1]:1"}}
	sibling := &stubSession{addr: stubAddr{"[2001:db8:1:2::1]:1"}}

	require.True(t, allowRegistration(t.Context(), budget, first))
	require.False(t, allowRegistration(t.Context(), budget, sibling), "the /48 budget did not hold")

	assert.True(t, budget.net.Allow(ratelimit.NetKey("2001:db8:1:2::1")), "the refused /64 spent its own budget")
	for range 4 {
		assert.True(t, budget.global.Allow(""), "the refusal spent the server-wide budget")
	}
	assert.False(t, budget.global.Allow(""))
}

// stubSession is the one ssh.Session double: enough for sessionModel's refusal paths
// (wish.Fatalf writes to Stderr, then exits and closes) and for the teardown helpers,
// which only look the session up in the registry. Embedding the interface leaves every
// other method nil on purpose: calling one is a bug in the test, not a silent pass.
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
	db.Authenticator
	user *db.User
	err  error
}

func (r stubUserRepo) LoadUserByFingerprint(context.Context, string) (*db.User, *db.PublicSSHKey, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.user, &db.PublicSSHKey{}, nil
}

func (stubUserRepo) UpdateUserActivity(context.Context, *db.User, *db.PublicSSHKey) error { return nil }

func newSessionDeps(tb testing.TB, repo db.Authenticator) Deps {
	tb.Helper()
	return Deps{
		Config:       &config.Config{},
		Auth:         repo,
		LobbyManager: lobby.NewManager(tb.Context(), nil, game.NewRegistry()),
		GameRegistry: game.NewRegistry(),
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
		repo       db.Authenticator
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
			name:    "the session state was already torn down",
			session: &stubSession{addr: stubAddr{"10.0.0.4:1"}, pubKey: testPublicKey(t), user: "ghost"},
			// No expectations: any lookup fails the test. A session that is going to be
			// refused must not first register an account and spend the network's budget.
			repo:       new(MockUserRepository),
			tracker:    NewSessionTracker(0),
			storeState: false,
			wantOutput: "could not be started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := &sessionRegistry{}
			if tt.storeState {
				reg.store(tt.session, &sessionState{traceCtx: t.Context()})
			}
			before := tt.tracker.Count()

			deps := newSessionDeps(t, tt.repo)
			deps.Tracker = tt.tracker
			limiter := testBudget(5, 5, 5)
			model := sessionModel(deps, reg, limiter)(tt.session)

			assert.Nil(t, model, "a refused session must not be handed to bubbletea")
			assert.Contains(t, tt.session.errOut.String(), tt.wantOutput)
			assert.Equal(t, 1, tt.session.exited, "the client is left waiting on a session that never starts")
			assert.Equal(t, before, tt.tracker.Count(), "a refused session kept a tracker slot")
		})
	}
}

// A refusal has to land on the session span as well as the client's screen, or a
// trace shows a session that simply ended without saying why.
func TestFailSessionf_MarksTheSessionSpan(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(t.Context(), "ssh.session")
	s := &stubSession{addr: stubAddr{"10.0.0.12:1"}}
	reg := &sessionRegistry{}
	reg.store(s, &sessionState{traceCtx: ctx, span: span})

	reg.failSessionf(s, "rejected_full", errServerFull, "full\n")
	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	assert.Equal(t, codes.Error, ended[0].Status().Code)
	assert.Equal(t, "rejected_full", ended[0].Status().Description, "the span does not say which refusal it was")
	require.Len(t, ended[0].Events(), 1, "the refusal's error was not recorded on the span")
	assert.Equal(t, "full\n", s.errOut.String())
	assert.Equal(t, 1, s.exited)
}

// Routine connect/disconnect logs carry the network the limiter counts, never the
// address; the warn-level refusals carry the address. Neither may fail on a session
// that has no usable address.
func TestLoggedAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     net.Addr
		wantNet  string
		wantAddr string
	}{
		{name: "ipv4", addr: stubAddr{"198.51.100.7:22"}, wantNet: "198.51.100.7", wantAddr: "198.51.100.7:22"},
		{name: "ipv6 is logged as its /64", addr: stubAddr{"[2001:db8::1]:22"}, wantNet: "2001:db8::/64", wantAddr: "[2001:db8::1]:22"},
		{name: "no port", addr: stubAddr{"198.51.100.7"}, wantNet: "unknown", wantAddr: "198.51.100.7"},
		{name: "no address", addr: nil, wantNet: "unknown", wantAddr: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantNet, clientNet(tt.addr))
			assert.Equal(t, tt.wantAddr, addrString(tt.addr))
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
	st := &sessionState{traceCtx: t.Context()}
	reg := &sessionRegistry{}
	reg.store(s, st)

	tracker := NewSessionTracker(0)
	deps := newSessionDeps(t, stubUserRepo{user: user})
	limiter := testBudget(5, 5, 5)

	deps.Tracker = tracker
	model := sessionModel(deps, reg, limiter)(s)
	require.NotNil(t, model)
	t.Cleanup(func() { st.model.Close() })

	assert.Equal(t, user, st.user, "teardown skips a session with no user, so it would free nothing")
	assert.NotZero(t, st.gen)
	assert.NotNil(t, st.model, "closeSessionModel would leave the view's subscriptions running")
	assert.True(t, release(tracker, user.ID, st.gen), "the session does not own the slot it claimed")
	assert.Zero(t, s.exited, "an accepted session must not be hung up on")
}

// sessionProgram is what wish calls; handing it a refused session must produce no
// program rather than a bubbletea run over a nil model.
func TestSessionProgram_RefusedSessionGetsNoProgram(t *testing.T) {
	t.Parallel()

	s := &stubSession{addr: stubAddr{"10.0.0.10:1"}}
	reg := &sessionRegistry{}
	reg.store(s, &sessionState{traceCtx: t.Context()})

	deps := newSessionDeps(t, stubUserRepo{user: &db.User{ID: testutil.UID(5)}})
	deps.Tracker = NewSessionTracker(0)
	program := sessionProgram(deps, reg,
		testBudget(5, 5, 5))

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
			reg := &sessionRegistry{}
			reg.store(s, &sessionState{traceCtx: t.Context()})

			m := reportingModel{Model: panicModel{on: method}, session: s, reg: reg}
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
