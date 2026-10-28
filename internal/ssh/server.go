// Package ssh is the game's front door: the wish server, public-key identity and
// first-sight registration, the per-connection and per-network limits, and the
// session lifecycle that ties one ssh channel to one TUI, one tracker slot and one
// lobby seat.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"
	"github.com/Pieczasz/tty-cards/internal/tui"
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/activeterm"
	bm "charm.land/wish/v2/bubbletea"
	"github.com/charmbracelet/keygen"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	gossh "golang.org/x/crypto/ssh"
)

// ctxKey namespaces ssh.Context values to avoid collision with other middleware.
type ctxKey int

// Only per-connection facts belong on the ssh.Context: charm ssh hands one Context
// per TCP connection to every channel opened on it (and to its auth callbacks).
// Everything a session needs lives in sessionState, keyed by the session itself.
const (
	ctxKeyChannelCount ctxKey = iota
	// ctxKeyAuthRefused marks a connection already counted as refused at auth.
	ctxKeyAuthRefused
)

const (
	handshakeTimeout = 20 * time.Second
	connIdleTimeout  = 30 * time.Minute
	// sessionFPS caps each session's renderer ticks. bubbletea's default is 60, and
	// every tick compares the whole frame with the last one even when nothing
	// changed, so an idle session costs in proportion to fps x terminal cells. A card
	// table changes at human speed; 20 adds at most 50ms to a redraw. Measured with
	// cmd/loadtest, 1000 sessions (400 playing, 600 browsing at 120x40) on compose's
	// 3 cores: at 60, CPU p95 310% and 125 sessions lost; at 20, p95 174%, none lost,
	// p95 move round-trip 60ms. 10 was measured too, and rejected: 8% less CPU for a
	// median move round-trip of 60ms instead of 31ms.
	sessionFPS = 20
	// maxSessionsPerConnection bounds concurrent session channels on one connection.
	// Every channel loads the user with three preloads against a small connection
	// pool, so an unbounded client could exhaust the database from a single TCP
	// connection. Two allows the reconnect overlap a real client produces.
	maxSessionsPerConnection = 2
	maxEnvRequests           = 32
	maxEnvBytes              = 8 << 10
)

// sessionState is per-channel session state. It cannot live on the ssh.Context: that
// is shared by every channel of the connection, so a rejected second channel's
// teardown would close the first channel's model and free its tracker slot. Fields
// are written and read from the one goroutine that runs the session handler chain.
type sessionState struct {
	traceCtx context.Context
	span     trace.Span
	started  time.Time
	// user is set only once the session owns its tracker slot, so a nil user is
	// what tells the teardown there is no slot or seat to give up.
	user     *db.User
	model    router.Closer
	gen      uint64
	panicked bool
	// outcome is set by a refusal; finishSession falls back to how the session ended.
	outcome string
	input   *inputGuard
}

// filter is the session's bubbletea message filter: the input guard's bookkeeping and
// rules, then the terminal size cap.
func (st *sessionState) filter(m tea.Model, msg tea.Msg) tea.Msg {
	return capWindowSize(st.input.filter(m, msg))
}

// sessionRegistry maps a live ssh.Session to its state. sessionLifecycle creates the
// entry and deletes it last; sessionModel and the teardown defers look it up.
// NewServer makes one per server rather than sharing a package global, so servers
// in one process (every test that starts one) cannot see each other's sessions.
type sessionRegistry struct {
	states sync.Map
	// live counts session handlers still running. charm ssh runs each on a goroutine
	// that neither its Shutdown nor its Close waits for, so without this the drain
	// would move on while teardowns are still giving up seats. liveMu makes admitting
	// a handler and starting to drain atomic: a WaitGroup alone permits Add after
	// Wait has seen zero.
	liveMu   sync.Mutex
	draining bool
	live     sync.WaitGroup
	// drained is closed by the one waiter goroutine waitSessions ever starts.
	drained chan struct{}
}

// admit counts a session handler in, unless the server is draining.
func (reg *sessionRegistry) admit() bool {
	reg.liveMu.Lock()
	defer reg.liveMu.Unlock()
	if reg.draining {
		return false
	}
	reg.live.Add(1)
	return true
}

// waitSessions stops admitting session handlers, then blocks until every admitted
// one has returned or ctx ends. The waiter is started once and reused, so a caller
// that times out and calls again does not strand one per attempt.
func (reg *sessionRegistry) waitSessions(ctx context.Context) error {
	reg.liveMu.Lock()
	reg.draining = true
	if reg.drained == nil {
		ch := make(chan struct{})
		reg.drained = ch
		go func() {
			reg.live.Wait()
			close(ch)
		}()
	}
	drained := reg.drained
	reg.liveMu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("session teardowns still running: %w", context.Cause(ctx))
	}
}

func (reg *sessionRegistry) load(s ssh.Session) (*sessionState, bool) {
	st, ok := reg.states.Load(s)
	if !ok {
		return nil, false
	}
	state, ok := st.(*sessionState)
	return state, ok
}

func (reg *sessionRegistry) store(s ssh.Session, st *sessionState) {
	reg.states.Store(s, st)
}

func (reg *sessionRegistry) delete(s ssh.Session) {
	reg.states.Delete(s)
}

// Deps is what NewServer wires every session to. Tracker is required and is shared
// with the stats API, which counts who is online from it.
type Deps struct {
	Config *config.Config
	// Auth signs players in; Profiles and Leaderboard are handed on to the TUI.
	Auth         db.Authenticator
	Profiles     db.Profiles
	Leaderboard  db.Leaderboard
	LobbyManager *lobby.Manager
	// Games is what the menus offer and the game routes render; the lobby manager's
	// registry has to be built from the same entries.
	Games   []catalog.Entry
	Tracker *SessionTracker
}

// Server is the wish server plus the one thing charm ssh does not track: the session
// handlers, whose deferred teardown gives up seats and tracker slots.
type Server struct {
	*ssh.Server
	sessions *sessionRegistry
}

// WaitSessions refuses any session not yet started, then blocks until every running
// session handler has finished its teardown or ctx ends. Close is what ends them, so
// it is called after Close.
func (s *Server) WaitSessions(ctx context.Context) error {
	return s.sessions.waitSessions(ctx)
}

// errNoTracker refuses a server with no session tracker. A default one used to be
// built here, and the stats API, holding its own, then counted nobody online.
var errNoTracker = errors.New("ssh server needs a session tracker")

// NewServer builds the ssh server, creating its host key on first run. The caller
// owns the listener and calls Serve on it.
func NewServer(deps Deps) (*Server, error) {
	return newServer(deps, &sessionRegistry{})
}

func newServer(deps Deps, reg *sessionRegistry) (*Server, error) {
	if deps.Tracker == nil {
		return nil, errNoTracker
	}
	key, err := keygen.New(deps.Config.SSHKeyPath, keygen.WithKeyType(keygen.Ed25519))
	if err != nil {
		return nil, fmt.Errorf("load host key: %w", err)
	}

	if !key.KeyPairExists() {
		if err := key.WriteKeys(); err != nil {
			return nil, fmt.Errorf("write host key: %w", err)
		}
	}
	if err := ensureHostKeyPermissions(deps.Config.SSHKeyPath); err != nil {
		return nil, err
	}

	rateLimiter := ratelimit.New(deps.Config.RateLimitCount, deps.Config.RateLimitWindow)
	// Registration gets its own, far tighter budget than authentication. The auth
	// limiter is sized so an ssh-agent offering every key it holds still gets in;
	// minting an account is nothing like that, and each one is a permanent users row
	// plus a session slot, so a stranger must not be able to do it in a loop.
	registerBudget := newRegistrationBudget(deps.Config)

	// No wish.WithAddress: cmd/server builds the listener itself (LimitListener, and
	// PROXY protocol in front of it) and calls Serve on it, so an address here is
	// never read and only reads as if this were the one that binds.
	server, err := wish.NewServer(
		wish.WithHostKeyPEM(key.RawPrivateKey()),
		wish.WithIdleTimeout(connIdleTimeout),
		wish.WithPublicKeyAuth(rateLimitAuth(rateLimiter, ratelimit.New(1, authRefusalLogEvery),
			func(_ ssh.Context, _ ssh.PublicKey) bool { return true })),
		// wish runs the last middleware first, so this slice is in reverse execution
		// order. Connect/disconnect logging is sessionLifecycle's job rather than
		// wish's logging middleware: that one writes through the charm logger, which
		// bypasses slog and so never reaches the OTLP handler.
		wish.WithMiddleware(
			bm.MiddlewareWithProgramHandler(sessionProgram(deps, reg, registerBudget)),
			activeterm.Middleware(),
			sessionLifecycle(deps, reg),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create wish server: %w", err)
	}
	server.HandshakeTimeout = handshakeTimeout
	server.ChannelHandlers = map[string]ssh.ChannelHandler{
		"session": limitSessionChannels(ssh.DefaultSessionHandler),
	}

	return &Server{Server: server, sessions: reg}, nil
}

func ensureHostKeyPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat ssh host key: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("chmod ssh host key: %w", err)
		}
	}
	return nil
}

// clientHost is the host part of a remote address, for the limiter keys. An address
// that will not split cannot be keyed on - "host:port" gives every attempt its own
// bucket, which silently disables the limit - so callers get ok=false and must refuse
// rather than admit an unlimited client.
func clientHost(addr net.Addr) (string, bool) {
	if addr == nil {
		return "", false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", false
	}
	return host, true
}

// authRefusalLogEvery is how often one /48's throttled attempts reach the log at
// warn. Each offered key is an attempt, so a flood was a warn line per key, each with
// an address, in a store kept 14 days; the rest go to debug and the counter.
const authRefusalLogEvery = time.Minute

func rateLimitAuth(limiter, warned *ratelimit.SlidingWindow, next ssh.PublicKeyHandler) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		host, ok := clientHost(ctx.RemoteAddr())
		if !ok {
			if firstAuthRefusal(ctx) {
				observability.SSHAuthReject(ctx, "unkeyable")
				slog.WarnContext(ctx, "refusing ssh connection with an unkeyable remote address",
					"remote_addr", addrString(ctx.RemoteAddr()))
			}
			return false
		}
		if !limiter.Allow(ratelimit.NetKey(host)) {
			observability.RateLimitReject(ctx, "ssh")
			if firstAuthRefusal(ctx) {
				observability.SSHAuthReject(ctx, "ratelimit")
			}
			level := slog.LevelDebug
			if warned.Allow(ratelimit.WideKey(host)) {
				level = slog.LevelWarn
			}
			slog.Log(ctx, level, "rate limited ssh connection",
				"remote_addr", addrString(ctx.RemoteAddr()), "session_id", ctx.SessionID())
			return false
		}
		return next(ctx, key)
	}
}

// firstAuthRefusal reports whether this is the connection's first refused auth
// attempt: an ssh-agent offers every key it holds, each a call of its own, and the
// counter is for connections. One connection's auth callbacks run one at a time.
func firstAuthRefusal(ctx ssh.Context) bool {
	if ctx.Value(ctxKeyAuthRefused) != nil {
		return false
	}
	ctx.SetValue(ctxKeyAuthRefused, true)
	return true
}

// failSessionf reports a refusal on the session span as well as to the client, so a
// trace shows why a connection never got a screen.
func (reg *sessionRegistry) failSessionf(s ssh.Session, outcome string, err error, format string, args ...any) {
	var ctx context.Context = s.Context()
	if st, ok := reg.load(s); ok {
		st.outcome = outcome
		if st.traceCtx != nil {
			ctx = st.traceCtx
		}
		if st.span != nil {
			st.span.RecordError(err)
			st.span.SetStatus(codes.Error, outcome)
		}
	}
	observability.SSHSession(ctx, outcome)
	wish.Fatalf(s, format, args...)
}

func (reg *sessionRegistry) refuseFull(s ssh.Session, err error) {
	reg.failSessionf(s, "rejected_full", err,
		"The server is full right now - please try again in a few minutes.\n")
}

func sessionModel(
	deps Deps, reg *sessionRegistry, registerBudget registrationBudget,
) func(ssh.Session) tea.Model {
	return func(s ssh.Session) tea.Model {
		// Checked first: a session with no state has no teardown to release what it
		// claims, and must not register an account on its way to being refused.
		st, ok := reg.load(s)
		if !ok {
			err := errors.New("session state missing before the model was installed")
			slog.ErrorContext(s.Context(), "refusing a session whose state was already torn down",
				"error", err, "remote_addr", addrString(s.RemoteAddr()))
			reg.failSessionf(s, "rejected", err, "Your session could not be started - please reconnect.\n")
			return nil
		}
		traceCtx := st.traceCtx
		fingerprint, err := SessionFingerprint(s)
		if err != nil {
			reg.failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil
		}
		user, err := LoadOrRegisterUser(traceCtx, deps.Auth, s.User(), fingerprint,
			func() (func(), error) {
				// Before the budget is spent and the account made: a newcomer has no
				// live session to displace, so a full server would only refuse them
				// after the fact, leaving an account they never got to use.
				if deps.Tracker.Full() {
					return nil, errServerFull
				}
				refund, ok := allowRegistration(traceCtx, registerBudget, s)
				if !ok {
					return nil, errTooManyRegistrations
				}
				return refund, nil
			})
		if errors.Is(err, errServerFull) {
			reg.refuseFull(s, err)
			return nil
		}
		if err != nil {
			reg.failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil
		}
		// Built before the slot is claimed: a panic in here would otherwise strand a
		// tracker slot that nothing releases - and that account cannot connect again
		// until a restart.
		model := tui.New(tui.Deps{
			SessionCtx:   traceCtx,
			User:         *user,
			Profiles:     deps.Profiles,
			Leaderboard:  deps.Leaderboard,
			LobbyManager: deps.LobbyManager,
			Games:        deps.Games,
		})
		// The connection, not the session: closing a channel leaves the socket and any
		// other channel on it up until the peer notices.
		conn, _ := s.Context().Value(ssh.ContextKeyConn).(gossh.Conn)
		gen, err := deps.Tracker.Connect(user.ID, conn)
		if err != nil { // errServerFull, Connect's only refusal
			model.Close()
			reg.refuseFull(s, err)
			return nil
		}
		// Recorded the moment the slot is ours, before anything below can panic: the
		// teardown frees the slot and closes the model only for what it finds here.
		st.user = user
		st.gen = gen
		st.model = model
		observability.SSHSession(traceCtx, "accepted")
		// Only once the slot is ours: a refused session must not cancel the grace
		// timer holding this player's seat, and a displaced one has finished its
		// teardown by now, so any timer it armed is there to cancel.
		tui.ResumeSeat(model)

		// bubbletea's own recover prints to stderr and knows nothing about the span
		// or the metric, so reportingModel catches Init/Update/View first. Its
		// catching stays enabled all the same: it is the only thing covering the
		// goroutines bubbletea spawns per Cmd, and an unrecovered panic there takes
		// down the process for every connected player, not just this session.
		return reportingModel{Model: model, session: s, reg: reg}
	}
}

// reportingModel reports a panic in the TUI against the session's trace context and
// then quits, so the session ends the same way an idle removal does. It wraps the
// three methods bubbletea calls on the event-loop goroutine; a panic inside a Cmd
// runs on a goroutine bubbletea owns and is left to bubbletea's own recover.
type reportingModel struct {
	tea.Model
	session ssh.Session
	reg     *sessionRegistry
}

func (m reportingModel) Init() tea.Cmd {
	defer m.reg.reportPanic(m.session)
	return m.Model.Init()
}

func (m reportingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A recovered Update leaves the model on the state the panic interrupted, so
	// the session quits rather than rendering on from a half-applied message.
	defer m.reg.reportPanic(m.session)
	inner, cmd := m.Model.Update(msg)
	m.Model = inner
	return m, cmd
}

func (m reportingModel) View() tea.View {
	defer m.reg.reportPanic(m.session)
	return m.Model.View()
}

// reportPanic records a recovered panic and re-panics so the caller's own frame
// unwinds; the panic stops at bubbletea, which ends the program without taking the
// process with it. It must be a direct defer - a recover() one call deeper is nil.
//
// The notice goes to the session's stderr channel, not its stdout: bubbletea owns the
// screen and is about to tear it down. Without it the panic is written to the server's
// stderr and the client just sees the connection close on a frozen screen - the
// recoverSession message never runs, because nothing panics out of bubbletea.
func (reg *sessionRegistry) reportPanic(s ssh.Session) {
	r := recover()
	if r == nil {
		return
	}
	reg.recordSessionPanic(s, r)
	notifySessionPanic(s)
	panic(r)
}

const panicNotice = "\r\nAn unexpected internal error occurred. The administrators have been notified.\r\n"

func notifySessionPanic(s ssh.Session) {
	if w := s.Stderr(); w != nil {
		_, _ = io.WriteString(w, panicNotice)
	}
}

func sessionProgram(
	deps Deps, reg *sessionRegistry, registerBudget registrationBudget,
) bm.ProgramHandler {
	newModel := sessionModel(deps, reg, registerBudget)
	return func(s ssh.Session) *tea.Program {
		model := newModel(s)
		if model == nil {
			return nil
		}
		st, _ := reg.load(s) // sessionModel refuses a session without state
		// The session is the input only because charm ssh emulates the pty: an
		// allocated one would make MakeOptions read from its slave instead.
		st.input = newInputGuard(s, func() {
			slog.WarnContext(st.traceCtx, "closing ssh session: input over budget",
				"client_net", clientNet(s.RemoteAddr()))
			if w := s.Stderr(); w != nil {
				_, _ = io.WriteString(w, inputLimitNotice)
			}
		})
		pty, _, _ := s.Pty()
		// The renderer sizes its cell buffers from this and never passes it through
		// the filter, so it is capped here as well as there.
		w, h := capSize(pty.Window.Width, pty.Window.Height)
		return tea.NewProgram(model, append(bm.MakeOptions(s),
			tea.WithInput(st.input),
			tea.WithWindowSize(w, h),
			tea.WithFPS(sessionFPS),
			tea.WithFilter(st.filter))...)
	}
}

// filterSessionMsg makes bubbletea's messages safe for a remote terminal. A suspend
// sends SIGTSTP to the server's own process group, stopping every player's session
// on a machine nobody is at to resume it, so it is answered as if already resumed.
// A resize is sessionSize.filter's.
func filterSessionMsg(_ tea.Model, msg tea.Msg) tea.Msg {
	if _, ok := msg.(tea.SuspendMsg); ok {
		return tea.ResumeMsg{}
	}
	return msg
}

// limitSessionChannels enforces the per-connection channel cap where the channel is
// opened, before Accept. Counted in the middleware it bound nothing: a channel that
// never asks for a shell never reaches it, yet holds its request goroutine and
// buffers for as long as the client likes. The counter lives on the connection-scoped
// Context, whose own lock makes the first-writer race harmless.
func limitSessionChannels(next ssh.ChannelHandler) ssh.ChannelHandler {
	return func(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
		ctx.Lock()
		counter, ok := ctx.Value(ctxKeyChannelCount).(*atomic.Int32)
		if !ok {
			counter = new(atomic.Int32)
			ctx.SetValue(ctxKeyChannelCount, counter)
		}
		ctx.Unlock()

		if counter.Add(1) > maxSessionsPerConnection {
			counter.Add(-1)
			observability.SSHSession(ctx, "rejected_channel_limit")
			slog.WarnContext(ctx, "too many session channels on one connection",
				"remote_addr", conn.RemoteAddr().String(), "limit", maxSessionsPerConnection)
			_ = newChan.Reject(gossh.ResourceShortage, "too many sessions open on this connection")
			return
		}
		// The session handler returns once the channel's request stream closes, which
		// is the channel going away.
		defer counter.Add(-1)
		next(srv, conn, envCappedChannel{NewChannel: newChan}, ctx)
	}
}

// envCappedChannel refuses env requests past a count and byte budget. charm ssh keeps
// every accepted one for the session's life, so an unbounded stream is unbounded
// memory. A small budget rather than none: bubbletea reads TERM and colour hints
// from the environment.
type envCappedChannel struct {
	gossh.NewChannel
}

func (c envCappedChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	ch, reqs, err := c.NewChannel.Accept()
	if err != nil {
		return nil, nil, fmt.Errorf("accept session channel: %w", err)
	}
	out := make(chan *gossh.Request)
	go func() {
		defer close(out)
		count, size := 0, 0
		for req := range reqs {
			if req.Type == "env" {
				count++
				size += len(req.Payload)
				if count > maxEnvRequests || size > maxEnvBytes {
					_ = req.Reply(false, nil)
					continue
				}
			}
			out <- req
		}
	}()
	return ch, out, nil
}

func sessionLifecycle(deps Deps, reg *sessionRegistry) wish.Middleware {
	return func(sh ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			// A handler charm ssh started just before Close can get here after the
			// drain stopped counting; it refuses itself rather than run untracked.
			if !reg.admit() {
				wish.Fatalln(s, "The server is restarting - please reconnect in a moment.")
				return
			}
			// Registered first, so it runs last: the drain is over only once the
			// whole teardown below has run.
			defer reg.live.Done()
			st := reg.startSession(s)
			defer reg.finishSession(s, st)
			defer reg.recoverSession(s)
			defer reg.releaseSession(s, deps)
			defer reg.closeSessionModel(s)

			sh(s)
		}
	}
}

func (reg *sessionRegistry) startSession(s ssh.Session) *sessionState {
	pty, _, _ := s.Pty()
	tracer := otel.Tracer("tty-cards/ssh")
	//nolint:spancheck // the span outlives this function: finishSession ends it as the last deferred step
	ctx, span := tracer.Start(s.Context(), "ssh.session",
		trace.WithAttributes(
			// No client address here: the span also carries the username once the
			// player is known, and joining the two is exactly the record a trace store
			// should not hold for 48 hours. Abuse investigation has the warn-level logs.
			attribute.String("client_version", s.Context().ClientVersion()),
			attribute.Int("terminal.width", pty.Window.Width),
			attribute.Int("terminal.height", pty.Window.Height),
		))

	st := &sessionState{traceCtx: ctx, span: span, started: time.Now()}
	reg.store(s, st)

	slog.InfoContext(ctx, "ssh session connected",
		"client_net", clientNet(s.RemoteAddr()),
		"client_version", s.Context().ClientVersion(),
	)
	return st //nolint:spancheck // the span outlives this call: finishSession ends it, as the outermost deferred step of sessionLifecycle
}

func (reg *sessionRegistry) finishSession(s ssh.Session, st *sessionState) {
	defer reg.delete(s)

	outcome := st.outcome
	switch {
	case st.panicked:
		outcome = "panic"
	case st.input != nil && st.input.tripped.Load():
		outcome = "input_limit"
	case outcome == "":
		outcome = "normal"
	}
	elapsed := time.Since(st.started)

	observability.SSHSessionEnded(st.traceCtx, elapsed, outcome)
	slog.InfoContext(st.traceCtx, "ssh session disconnected",
		"client_net", clientNet(s.RemoteAddr()),
		"client_version", s.Context().ClientVersion(),
		"duration_seconds", elapsed.Seconds(),
		"outcome", outcome,
	)

	if st.user != nil {
		st.span.SetAttributes(attribute.String("user", st.user.Username))
	}
	st.span.End()
}

func (reg *sessionRegistry) recoverSession(s ssh.Session) {
	r := recover()
	if r == nil {
		return
	}
	reg.recordSessionPanic(s, r)
	wish.Fatalf(s, "%s", panicNotice)
}

// recordSessionPanic puts a recovered panic on the session span, the metric and the
// log, all against the session's own trace context. It reports only: whether the
// session can be told about it is the caller's business.
func (reg *sessionRegistry) recordSessionPanic(s ssh.Session, r any) {
	err := fmt.Errorf("panic during ssh session: %v", r)
	var ctx context.Context = s.Context()
	if st, ok := reg.load(s); ok {
		if st.traceCtx != nil {
			ctx = st.traceCtx
		}
		st.panicked = true
		if st.span != nil {
			st.span.RecordError(err, trace.WithStackTrace(true))
			st.span.SetStatus(codes.Error, "panic during ssh session")
		}
	}
	observability.SSHPanicRecovered(ctx)
	slog.ErrorContext(ctx, "critical panic recovered during ssh session",
		"panic", r,
		"remote_addr", addrString(s.RemoteAddr()),
	)
}

func (reg *sessionRegistry) closeSessionModel(s ssh.Session) {
	if st, ok := reg.load(s); ok && st.model != nil {
		st.model.Close()
	}
}

// releaseSession gives up the seat and the tracker slot as one step under the
// account's tracker lock. Separately, a reconnect could take the slot and resume the seat in
// between, and this session's DisconnectPlayer would then arm a grace timer on the
// seat the replacement is playing. A displaced session (stale generation) touches
// neither.
func (reg *sessionRegistry) releaseSession(s ssh.Session, deps Deps) {
	st, ok := reg.load(s)
	if !ok || st.user == nil {
		return
	}
	// DisconnectPlayer, not LeaveLobby: a dropped session keeps its mid-game seat
	// for the grace window, so a reconnect resumes the match instead of forfeiting.
	deps.Tracker.ReleaseWith(st.user.ID, st.gen, func() {
		deps.LobbyManager.DisconnectPlayer(lobby.NewPlayer(st.user))
	})
}

// registrationBudget is what a new account spends, narrowest first: its /64, its /48
// (a free tunnel is 65,536 /64s) and the server as a whole, for an attacker with more
// networks than any per-network budget can see. Returning players spend none of it.
type registrationBudget struct {
	net, wide, global *ratelimit.SlidingWindow
}

func newRegistrationBudget(cfg *config.Config) registrationBudget {
	return registrationBudget{
		net:    ratelimit.New(cfg.RegistrationLimit, cfg.RegistrationWindow),
		wide:   ratelimit.New(cfg.RegistrationLimitWide, cfg.RegistrationWindow),
		global: ratelimit.New(cfg.RegistrationGlobalLimit, cfg.RegistrationWindow),
	}
}

// allowRegistration answers whether this network may mint another account. An
// address that cannot be keyed is refused: registration is the one path where
// admitting an unmeterable client is worse than turning a real player away.
func allowRegistration(ctx context.Context, budget registrationBudget, s ssh.Session) (refund func(), ok bool) {
	host, ok := clientHost(s.RemoteAddr())
	if !ok {
		return nil, false
	}
	spends := []struct {
		limiter *ratelimit.SlidingWindow
		key     string
		name    string
	}{
		{budget.net, ratelimit.NetKey(host), "ssh_register"},
		{budget.wide, ratelimit.WideKey(host), "ssh_register_wide"},
		{budget.global, "", "ssh_register_global"},
	}
	for i, spend := range spends {
		if spend.limiter.Allow(spend.key) {
			continue
		}
		// Refused, so the account is never made: the narrower budgets get back what
		// they just admitted, or a flood filling the global ceiling would also lock
		// every real player who retried out of their own network's budget.
		for _, admitted := range spends[:i] {
			admitted.limiter.Refund(admitted.key)
		}
		observability.RateLimitReject(ctx, spend.name)
		return nil, false
	}
	// Given back if the registration then fails (a taken name, a racing key): no
	// account was made, so it must not cost the network one of its few.
	return func() {
		for _, spent := range spends {
			spent.limiter.Refund(spent.key)
		}
	}, true
}

// clientNet is what the routine connect/disconnect logs record instead of the address:
// the same /64 the rate limiter keys on. It is enough to spot a flood or a broken
// client and it stops every ordinary session from writing a personal identifier into
// a store with a retention policy. Warn-level refusals keep the full address; those
// are the events an operator investigates.
func clientNet(addr net.Addr) string {
	if host, ok := clientHost(addr); ok {
		return ratelimit.NetKey(host)
	}
	return "unknown"
}

// addrString is the remote_addr the warn-level logs record: the full address, or
// "unknown" for a session with none.
func addrString(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	return addr.String()
}
