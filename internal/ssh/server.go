// Package ssh contains implementation for setting up ssh auth, middleware, and
// server setup.
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

	"uuid"

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
	// Registration gets its own, far tighter budget than authentication. The auth
	// limiter is sized so an ssh-agent offering every key it holds still gets in;
	// minting an account is nothing like that, and each one is a permanent users row
	// plus a session slot, so a stranger must not be able to do it in a loop.
	registrationLimit  = 5
	registrationWindow = time.Hour
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
	user     *db.User
	model    interface{ Close() }
	owns     bool
	gen      uint64
	panicked bool
}

// sessionStates maps a live ssh.Session to its state. sessionLifecycle creates the
// entry and deletes it last; sessionModel and the teardown defers look it up.
var sessionStates sync.Map

func lookupSessionState(s ssh.Session) (*sessionState, bool) {
	st, ok := sessionStates.Load(s)
	if !ok {
		return nil, false
	}
	state, ok := st.(*sessionState)
	return state, ok
}

// ErrServerFull is Connect's capacity refusal.
var ErrServerFull = errors.New("server is at capacity")

// trackedSession is the live session for an account: the generation its teardown
// must match, and the handle used to hang up on it when a newer one displaces it.
type trackedSession struct {
	gen  uint64
	conn io.Closer
}

type SessionTracker struct {
	mu     sync.Mutex
	active map[uuid.UUID]trackedSession
	next   uint64
	// maxSessions is the player-visible capacity: Connect refuses beyond it with a
	// message, unlike the TCP-level LimitListener, which silently stops accepting.
	// Zero means unlimited.
	maxSessions int
}

func NewSessionTracker(maxSessions int) *SessionTracker {
	return &SessionTracker{
		active:      make(map[uuid.UUID]trackedSession),
		maxSessions: maxSessions,
	}
}

// Connect registers userID and returns a generation. A second Connect for the same
// account displaces the first: half-open TCP otherwise blocks reconnect for the whole
// mid-game grace window. Release with a stale generation is a no-op.
//
// conn is the connection the displaced session is hung up on. Without closing it,
// the account keeps every connection it ever opened until each one's TCP dies, so the
// per-account limit becomes advisory: Count still counts accounts, but each one can
// hold any number of live TUIs and lobby subscriptions.
func (t *SessionTracker) Connect(userID uuid.UUID, conn io.Closer) (uint64, error) {
	t.mu.Lock()
	t.next++
	gen := t.next
	prev, exists := t.active[userID]
	if !exists && t.maxSessions > 0 && len(t.active) >= t.maxSessions {
		t.mu.Unlock()
		return 0, ErrServerFull
	}
	t.active[userID] = trackedSession{gen: gen, conn: conn}
	if !exists {
		observability.SSHSessionsActive.Add(1)
	}
	t.mu.Unlock()

	// Outside the lock: Close writes to the network, and a wedged peer must not hold
	// every other account's Connect behind it. The displaced session's own teardown
	// is already harmless - Release only frees a slot for the live generation.
	if exists && prev.conn != nil {
		_ = prev.conn.Close()
	}
	return gen, nil
}

func (t *SessionTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active)
}

// Release frees the slot only when gen is still the live generation. A displaced
// session's teardown must not drop the replacement or start a disconnect grace.
func (t *SessionTracker) Release(userID uuid.UUID, gen uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[userID].gen != gen {
		return false
	}
	delete(t.active, userID)
	observability.SSHSessionsActive.Add(-1)
	return true
}

// ReleaseWith runs fn and then frees the slot, both under the tracker lock, only if
// gen is still the live generation. Holding the lock across fn is the point: a
// reconnect's Connect waits for the old session's teardown to finish, so the
// teardown can never act on a seat the new session has already resumed. That makes
// the lock order tracker, then lobby manager; nothing takes them the other way.
func (t *SessionTracker) ReleaseWith(userID uuid.UUID, gen uint64, fn func()) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[userID].gen != gen {
		return false
	}
	fn()
	delete(t.active, userID)
	observability.SSHSessionsActive.Add(-1)
	return true
}

// Owns reports whether gen is still the live generation for userID.
func (t *SessionTracker) Owns(userID uuid.UUID, gen uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[userID].gen == gen
}

// Disconnect is Release without a generation check - tests and paths that never
// displaced. Prefer Release from session teardown.
func (t *SessionTracker) Disconnect(userID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.active[userID]; ok {
		delete(t.active, userID)
		observability.SSHSessionsActive.Add(-1)
	}
}

type ServerDependencies struct {
	Config         *config.Config
	UserRepository db.UserRepository
	LobbyManager   *lobby.Manager
	GameRegistry   *game.Registry
	Tracker        *SessionTracker
}

func SetupServer(deps ServerDependencies) (*ssh.Server, error) {
	key, err := keygen.New(deps.Config.SSHKeyPath, keygen.WithKeyType(keygen.Ed25519))
	if err != nil {
		return nil, fmt.Errorf("generating a keygen pair error: %w", err)
	}

	if !key.KeyPairExists() {
		if err := key.WriteKeys(); err != nil {
			return nil, fmt.Errorf("error while saving keypair to disk: %w", err)
		}
	}
	if err := ensureHostKeyPermissions(deps.Config.SSHKeyPath); err != nil {
		return nil, err
	}

	tracker := deps.Tracker
	if tracker == nil {
		tracker = NewSessionTracker(deps.Config.MaxConnections)
	}
	rateLimiter := ratelimit.NewSlidingWindowLimiter(deps.Config.RateLimitCount, deps.Config.RateLimitWindow)
	registerLimiter := ratelimit.NewSlidingWindowLimiter(registrationLimit, registrationWindow)

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
			bm.MiddlewareWithProgramHandler(sessionProgram(deps, tracker, registerLimiter)),
			activeterm.Middleware(),
			sessionLifecycle(deps, tracker),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("error while setting up wish ssh server: %w", err)
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

func rateLimitAuth(limiter *ratelimit.SlidingWindowLimiter, next ssh.PublicKeyHandler) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		host, ok := netKeyFor(ctx.RemoteAddr())
		if !ok {
			observability.SSHSession(ctx, "rejected_ratelimit")
			slog.WarnContext(ctx, "refusing ssh connection with an unkeyable remote address",
				"remote_addr", ctx.RemoteAddr())
			return false
		}
		if !limiter.Allow(host) {
			observability.RateLimitReject(ctx, "ssh")
			observability.SSHSession(ctx, "rejected_ratelimit")
			slog.WarnContext(ctx, "rate limited ssh connection",
				"remote_addr", ctx.RemoteAddr().String(), "session_id", ctx.SessionID())
			return false
		}
		return next(ctx, key)
	}
}

func sessionTraceContext(s ssh.Session) context.Context {
	if st, ok := lookupSessionState(s); ok && st.traceCtx != nil {
		return st.traceCtx
	}
	return s.Context()
}

// failSession reports a refusal on the session span as well as to the client, so a
// trace shows why a connection never got a screen.
func failSessionf(s ssh.Session, outcome string, err error, format string, args ...any) {
	ctx := sessionTraceContext(s)
	if st, ok := lookupSessionState(s); ok && st.span != nil {
		st.span.RecordError(err)
		st.span.SetStatus(codes.Error, outcome)
	}
	observability.SSHSession(ctx, outcome)
	wish.Fatalf(s, format, args...)
}

func sessionModel(
	deps ServerDependencies, tracker *SessionTracker, registerLimiter *ratelimit.SlidingWindowLimiter,
) func(ssh.Session) (tea.Model, []tea.ProgramOption) {
	return func(s ssh.Session) (tea.Model, []tea.ProgramOption) {
		traceCtx := sessionTraceContext(s)
		fingerprint, err := AuthenticateSession(s)
		if err != nil {
			failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil, nil
		}
		user, err := LoadOrRegisterUser(traceCtx, deps.UserRepository, s.User(), fingerprint,
			func() bool { return allowRegistration(traceCtx, registerLimiter, s) })
		if err != nil {
			failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil, nil
		}
		// Built before the slot is claimed: a panic in here, or a session whose state
		// has already been torn down, would otherwise strand a tracker slot that
		// nothing releases - and that account cannot connect again until a restart.
		model := tui.Model(tui.ModelDependencies{
			SessionCtx:   traceCtx,
			User:         *user,
			UserRepo:     deps.UserRepository,
			LobbyManager: deps.LobbyManager,
			GameRegistry: deps.GameRegistry,
		})
		st, ok := lookupSessionState(s)
		if !ok {
			err := errors.New("session state missing before the model was installed")
			slog.ErrorContext(traceCtx, err.Error(), "remote_addr", s.RemoteAddr().String())
			model.Close()
			failSessionf(s, "rejected", err, "Your session could not be started - please reconnect.\n")
			return nil, nil
		}

		// The connection, not the session: closing a channel leaves the socket and any
		// other channel on it up until the peer notices.
		conn, _ := s.Context().Value(ssh.ContextKeyConn).(gossh.Conn)
		gen, err := tracker.Connect(user.ID, conn)
		switch {
		case errors.Is(err, ErrServerFull):
			model.Close()
			failSessionf(s, "rejected_full", err,
				"The server is full right now - please try again in a few minutes.\n")
			return nil, nil
		case err != nil:
			model.Close()
			failSessionf(s, "rejected", err, "%v\n", err)
			return nil, nil
		}
		observability.SSHSession(traceCtx, "accepted")
		// Only once the slot is ours: a refused session must not cancel the grace
		// timer holding this player's seat, and a displaced one has finished its
		// teardown by now, so any timer it armed is there to cancel.
		tui.ResumeSeat(model)

		st.owns = true
		st.user = user
		st.gen = gen
		st.model = model

		// bubbletea's own recover prints to stderr and knows nothing about the span
		// or the metric, so reportingModel catches Init/Update/View first. Its
		// catching stays enabled all the same: it is the only thing covering the
		// goroutines bubbletea spawns per Cmd, and an unrecovered panic there takes
		// down the process for every connected player, not just this session.
		return reportingModel{Model: model, session: s}, nil
	}
}

// reportingModel reports a panic in the TUI against the session's trace context and
// then quits, so the session ends the same way an idle removal does. It wraps the
// three methods bubbletea calls on the event-loop goroutine; a panic inside a Cmd
// runs on a goroutine bubbletea owns and is left to bubbletea's own recover.
type reportingModel struct {
	tea.Model
	session ssh.Session
}

func (m reportingModel) Init() tea.Cmd {
	defer reportPanic(m.session)
	return m.Model.Init()
}

func (m reportingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A recovered Update leaves the model on the state the panic interrupted, so
	// the session quits rather than rendering on from a half-applied message.
	defer reportPanic(m.session)
	inner, cmd := m.Model.Update(msg)
	m.Model = inner
	return m, cmd
}

func (m reportingModel) View() tea.View {
	defer reportPanic(m.session)
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
func reportPanic(s ssh.Session) {
	r := recover()
	if r == nil {
		return
	}
	recordSessionPanic(s, r)
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
	deps ServerDependencies, tracker *SessionTracker, registerLimiter *ratelimit.SlidingWindowLimiter,
) bm.ProgramHandler {
	newModel := sessionModel(deps, tracker, registerLimiter)
	return func(s ssh.Session) *tea.Program {
		model, opts := newModel(s)
		if model == nil {
			return nil
		}
		opts = append(opts, bm.MakeOptions(s)...)
		return tea.NewProgram(model, append(opts, tea.WithFilter(clampWindowSize))...)
	}
}

func clampWindowSize(_ tea.Model, msg tea.Msg) tea.Msg {
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

func sessionLifecycle(deps ServerDependencies, tracker *SessionTracker) wish.Middleware {
	return func(sh ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			st := startSession(s)
			defer finishSession(s, st)
			defer recoverSession(s)
			defer releaseSession(s, deps, tracker)
			defer closeSessionModel(s)

			sh(s)
		}
	}
}

func startSession(s ssh.Session) *sessionState {
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
	sessionStates.Store(s, st)

	slog.InfoContext(ctx, "ssh session connected",
		"client_net", clientNet(s.RemoteAddr()),
		"client_version", s.Context().ClientVersion(),
	)
	return st //nolint:spancheck // the span outlives this call: finishSession ends it, as the outermost deferred step of sessionLifecycle
}

func finishSession(s ssh.Session, st *sessionState) {
	defer sessionStates.Delete(s)

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

func recoverSession(s ssh.Session) {
	r := recover()
	if r == nil {
		return
	}
	recordSessionPanic(s, r)
	wish.Fatalf(s, "%s", panicNotice)
}

// recordSessionPanic puts a recovered panic on the session span, the metric and the
// log, all against the session's own trace context. It reports only: whether the
// session can be told about it is the caller's business.
func recordSessionPanic(s ssh.Session, r any) {
	err := fmt.Errorf("panic during ssh session: %v", r)
	ctx := sessionTraceContext(s)
	if st, ok := lookupSessionState(s); ok {
		st.panicked = true
		if st.span != nil {
			st.span.RecordError(err, trace.WithStackTrace(true))
			st.span.SetStatus(codes.Error, "panic during ssh session")
		}
	}
	observability.SSHPanicRecovered(ctx)
	slog.ErrorContext(ctx, "critical panic recovered during ssh session",
		"panic", r,
		"remote_addr", s.RemoteAddr().String(),
	)
}

func closeSessionModel(s ssh.Session) {
	if st, ok := lookupSessionState(s); ok && st.model != nil {
		st.model.Close()
	}
}

// releaseSession gives up the seat and the tracker slot as one step under the
// tracker lock. Separately, a reconnect could take the slot and resume the seat in
// between, and this session's DisconnectPlayer would then arm a grace timer on the
// seat the replacement is playing. A displaced session (stale generation) touches
// neither.
func releaseSession(s ssh.Session, deps ServerDependencies, tracker *SessionTracker) {
	st, ok := lookupSessionState(s)
	if !ok || !st.owns || st.user == nil {
		return
	}
	// DisconnectPlayer, not LeaveLobby: a dropped session keeps its mid-game seat
	// for the grace window, so a reconnect resumes the match instead of forfeiting.
	tracker.ReleaseWith(st.user.ID, st.gen, func() {
		deps.LobbyManager.DisconnectPlayer(lobby.NewPlayer(st.user))
	})
}

// allowRegistration answers whether this network may mint another account. An
// address that cannot be keyed is refused: registration is the one path where
// admitting an unmeterable client is worse than turning a real player away.
func allowRegistration(
	ctx context.Context, limiter *ratelimit.SlidingWindowLimiter, s ssh.Session,
) bool {
	if limiter == nil {
		return true
	}
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
