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

	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/observability"
	"github.com/Pieczasz/terminal-card/internal/ratelimit"
	"github.com/Pieczasz/terminal-card/internal/tui"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

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

// ctxKeyChannelCount is the only thing that belongs on the ssh.Context: charm ssh
// hands one Context per TCP connection to every channel opened on it, which is
// exactly the scope a per-connection counter wants. Everything else a session needs
// lives in sessionState, keyed by the session itself.
const ctxKeyChannelCount ctxKey = iota

const (
	handshakeTimeout  = 20 * time.Second
	connIdleTimeout   = 30 * time.Minute
	maxTerminalWidth  = 2000
	maxTerminalHeight = 600
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
}

// sessionRegistry maps a live ssh.Session to its state. sessionLifecycle creates the
// entry and deletes it last; sessionModel and the teardown defers look it up.
// NewServer makes one per server rather than sharing a package global, so servers
// in one process (every test that starts one) cannot see each other's sessions.
type sessionRegistry struct {
	states sync.Map
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
	GameRegistry *game.Registry
	Tracker      *SessionTracker
}

// ErrNoTracker refuses a server with no session tracker. A default one used to be
// built here, and the stats API, holding its own, then counted nobody online.
var ErrNoTracker = errors.New("ssh server needs a session tracker")

// NewServer builds the ssh server, creating its host key on first run. The caller
// owns the listener and calls Serve on it.
func NewServer(deps Deps) (*ssh.Server, error) {
	if deps.Tracker == nil {
		return nil, ErrNoTracker
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

	reg := &sessionRegistry{}
	rateLimiter := ratelimit.New(deps.Config.RateLimitCount, deps.Config.RateLimitWindow)
	// Registration gets its own, far tighter budget than authentication. The auth
	// limiter is sized so an ssh-agent offering every key it holds still gets in;
	// minting an account is nothing like that, and each one is a permanent users row
	// plus a session slot, so a stranger must not be able to do it in a loop.
	registerLimiter := ratelimit.New(deps.Config.RegistrationLimit, deps.Config.RegistrationWindow)

	// No wish.WithAddress: cmd/server builds the listener itself (LimitListener, and
	// PROXY protocol in front of it) and calls Serve on it, so an address here is
	// never read and only reads as if this were the one that binds.
	server, err := wish.NewServer(
		wish.WithHostKeyPEM(key.RawPrivateKey()),
		wish.WithIdleTimeout(connIdleTimeout),
		wish.WithPublicKeyAuth(rateLimitAuth(rateLimiter, func(_ ssh.Context, _ ssh.PublicKey) bool {
			return true
		})),
		boundedPty(),
		// wish runs the last middleware first, so this slice is in reverse execution
		// order. Connect/disconnect logging is sessionLifecycle's job rather than
		// wish's logging middleware: that one writes through the charm logger, which
		// bypasses slog and so never reaches the OTLP handler.
		wish.WithMiddleware(
			bm.MiddlewareWithProgramHandler(sessionProgram(deps, reg, registerLimiter)),
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

	return server, nil
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

// netKeyFor is the limiter key for a remote address: the /64 for IPv6, the address
// itself for IPv4. An address that will not split cannot be keyed on - "host:port"
// gives every attempt its own bucket, which silently disables the limit - so callers
// get ok=false and must refuse rather than admit an unlimited client.
func netKeyFor(addr net.Addr) (string, bool) {
	if addr == nil {
		return "", false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", false
	}
	return ratelimit.NetKey(host), true
}

func rateLimitAuth(limiter *ratelimit.SlidingWindow, next ssh.PublicKeyHandler) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		host, ok := netKeyFor(ctx.RemoteAddr())
		if !ok {
			observability.SSHSession(ctx, "rejected_ratelimit")
			slog.WarnContext(ctx, "refusing ssh connection with an unkeyable remote address",
				"remote_addr", addrString(ctx.RemoteAddr()))
			return false
		}
		if !limiter.Allow(host) {
			observability.RateLimitReject(ctx, "ssh")
			observability.SSHSession(ctx, "rejected_ratelimit")
			slog.WarnContext(ctx, "rate limited ssh connection",
				"remote_addr", addrString(ctx.RemoteAddr()), "session_id", ctx.SessionID())
			return false
		}
		return next(ctx, key)
	}
}

func (reg *sessionRegistry) sessionTraceContext(s ssh.Session) context.Context {
	if st, ok := reg.load(s); ok && st.traceCtx != nil {
		return st.traceCtx
	}
	return s.Context()
}

// failSessionf reports a refusal on the session span as well as to the client, so a
// trace shows why a connection never got a screen.
func (reg *sessionRegistry) failSessionf(s ssh.Session, outcome string, err error, format string, args ...any) {
	var ctx context.Context = s.Context()
	if st, ok := reg.load(s); ok {
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

func sessionModel(
	deps Deps, reg *sessionRegistry, registerLimiter *ratelimit.SlidingWindow,
) func(ssh.Session) tea.Model {
	return func(s ssh.Session) tea.Model {
		traceCtx := reg.sessionTraceContext(s)
		fingerprint, err := SessionFingerprint(s)
		if err != nil {
			reg.failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil
		}
		user, err := LoadOrRegisterUser(traceCtx, deps.Auth, s.User(), fingerprint,
			func() bool { return allowRegistration(traceCtx, registerLimiter, s) })
		if err != nil {
			reg.failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil
		}
		// Built before the slot is claimed: a panic in here, or a session whose state
		// has already been torn down, would otherwise strand a tracker slot that
		// nothing releases - and that account cannot connect again until a restart.
		model := tui.New(tui.Deps{
			SessionCtx:   traceCtx,
			User:         *user,
			Profiles:     deps.Profiles,
			Leaderboard:  deps.Leaderboard,
			LobbyManager: deps.LobbyManager,
			GameRegistry: deps.GameRegistry,
		})
		st, ok := reg.load(s)
		if !ok {
			err := errors.New("session state missing before the model was installed")
			slog.ErrorContext(traceCtx, "refusing a session whose state was already torn down",
				"error", err, "remote_addr", addrString(s.RemoteAddr()))
			model.Close()
			reg.failSessionf(s, "rejected", err, "Your session could not be started - please reconnect.\n")
			return nil
		}

		// The connection, not the session: closing a channel leaves the socket and any
		// other channel on it up until the peer notices.
		conn, _ := s.Context().Value(ssh.ContextKeyConn).(gossh.Conn)
		gen, err := deps.Tracker.Connect(user.ID, conn)
		switch {
		case errors.Is(err, ErrServerFull):
			model.Close()
			reg.failSessionf(s, "rejected_full", err,
				"The server is full right now - please try again in a few minutes.\n")
			return nil
		case err != nil:
			model.Close()
			reg.failSessionf(s, "rejected", err, "%v\n", err)
			return nil
		}
		observability.SSHSession(traceCtx, "accepted")
		// Only once the slot is ours: a refused session must not cancel the grace
		// timer holding this player's seat, and a displaced one has finished its
		// teardown by now, so any timer it armed is there to cancel.
		tui.ResumeSeat(model)

		st.user = user
		st.gen = gen
		st.model = model

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

func boundedPty() ssh.Option {
	return func(srv *ssh.Server) error {
		srv.PtyCallback = func(_ ssh.Context, req ssh.Pty) bool {
			return req.Window.Width <= maxTerminalWidth && req.Window.Height <= maxTerminalHeight
		}
		return nil
	}
}

func sessionProgram(
	deps Deps, reg *sessionRegistry, registerLimiter *ratelimit.SlidingWindow,
) bm.ProgramHandler {
	newModel := sessionModel(deps, reg, registerLimiter)
	return func(s ssh.Session) *tea.Program {
		model := newModel(s)
		if model == nil {
			return nil
		}
		return tea.NewProgram(model, append(bm.MakeOptions(s), tea.WithFilter(filterSessionMsg))...)
	}
}

// filterSessionMsg makes bubbletea's messages safe for a remote terminal. A suspend
// sends SIGTSTP to the server's own process group, stopping every player's session
// on a machine nobody is at to resume it, so it is answered as if already resumed. A
// resize is clamped to what boundedPty accepts at pty-req, which a window-change
// request after it is not held to.
func filterSessionMsg(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg := msg.(type) {
	case tea.SuspendMsg:
		return tea.ResumeMsg{}
	case tea.WindowSizeMsg:
		msg.Width = min(msg.Width, maxTerminalWidth)
		msg.Height = min(msg.Height, maxTerminalHeight)
		return msg
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
		return ch, reqs, fmt.Errorf("accept session channel: %w", err)
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
	tracer := otel.Tracer("terminal-card/ssh")
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

	outcome := "normal"
	if st.panicked {
		outcome = "panic"
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
	ctx := reg.sessionTraceContext(s)
	if st, ok := reg.load(s); ok {
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
// tracker lock. Separately, a reconnect could take the slot and resume the seat in
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

// allowRegistration answers whether this network may mint another account. An
// address that cannot be keyed is refused: registration is the one path where
// admitting an unmeterable client is worse than turning a real player away.
func allowRegistration(
	ctx context.Context, limiter *ratelimit.SlidingWindow, s ssh.Session,
) bool {
	key, ok := netKeyFor(s.RemoteAddr())
	if !ok {
		return false
	}
	if !limiter.Allow(key) {
		observability.RateLimitReject(ctx, "ssh_register")
		return false
	}
	return true
}

// clientNet is what the routine connect/disconnect logs record instead of the address:
// the same /64 the rate limiter keys on. It is enough to spot a flood or a broken
// client and it stops every ordinary session from writing a personal identifier into
// a store with a retention policy. Warn-level refusals keep the full address; those
// are the events an operator investigates.
func clientNet(addr net.Addr) string {
	if key, ok := netKeyFor(addr); ok {
		return key
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
