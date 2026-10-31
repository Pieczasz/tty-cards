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
	"github.com/Pieczasz/tty-cards/internal/drain"
	"github.com/Pieczasz/tty-cards/internal/game"
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
	// player is set only once the session owns its tracker slot, so a nil player is
	// what tells the teardown there is no slot or seat to give up. It is the login's
	// db.User mapped to a seat once, here at the auth boundary, and the pointer the
	// TUI is built from. A ranked match replaces the TUI's copy (UpdatePlayer) but not
	// this one, so its Ratings go stale: the teardown reads only ID, UserID and Name.
	player   *game.Player
	model    router.Closer
	gen      uint64
	panicked bool
	// outcome is set by a refusal; finishSession falls back to how the session ended.
	outcome string
	input   *inputGuard
	size    sessionSize
	// program is set before the program runs, for the retier sweep to nudge.
	program atomic.Pointer[tea.Program]
	// run is the program's context and endRun ends it. charm ssh hands every session
	// on a connection that connection's context, so a session displaced by a newer
	// one on the same connection has nothing else that stops its program. run is
	// not derived from that context: a closing connection already quits the program
	// through wish, cleanly, where a cancelled run kills it and wish logs an ERROR.
	// Both are set just before Connect; endRun is safe from any goroutine, and
	// finishSession calls it to release run.
	run    context.Context
	endRun context.CancelFunc
}

// filter is the session's bubbletea message filter: the input guard's bookkeeping and
// rules, then the terminal size budget.
func (st *sessionState) filter(m tea.Model, msg tea.Msg) tea.Msg {
	return st.size.filter(st.input.filter(m, msg))
}

// sessionRegistry maps a live ssh.Session to its state. sessionLifecycle creates the
// entry and deletes it last; sessionModel and the teardown defers look it up.
// NewServer makes one per server rather than sharing a package global, so servers
// in one process (every test that starts one) cannot see each other's sessions.
type sessionRegistry struct {
	states sync.Map
	sizes  sizeBudget
	// live counts session handlers still running. charm ssh runs each on a goroutine
	// that neither its Shutdown nor its Close waits for, so without this the drain
	// would move on while teardowns are still giving up seats.
	live drain.Gate
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
	if !s.sessions.live.Wait(ctx) {
		return fmt.Errorf("session teardowns still running: %w", context.Cause(ctx))
	}
	return nil
}

// errNoTracker refuses a server with no session tracker. A default one used to be
// built here, and the stats API, holding its own, then counted nobody online.
var errNoTracker = errors.New("ssh server needs a session tracker")

// NewServer builds the ssh server, creating its host key on first run. The caller
// owns the listener and calls Serve on it.
func NewServer(deps Deps) (*Server, error) {
	return newServer(deps, newSessionRegistry(cellBudget, extraCellBudget))
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
		wish.WithPublicKeyAuth(rateLimitAuth(rateLimiter,
			ratelimit.New(authWideFactor*deps.Config.RateLimitCount, deps.Config.RateLimitWindow),
			ratelimit.New(1, authRefusalLogEvery),
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

// authWideFactor sizes the /48's auth budget against one network's. A free tunnel
// is 65,536 /64s, each a fresh network budget; 16 networks' worth still lets a site
// behind one /48 sign in with ssh-agents offering several keys each.
const authWideFactor = 16

// rateLimitAuth spends limiter's budget on the client's network and wide's on its
// /48, refusing when either is spent; warned only picks the refusal's log level.
func rateLimitAuth(limiter, wide, warned *ratelimit.SlidingWindow, next ssh.PublicKeyHandler) ssh.PublicKeyHandler {
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
		if !allowAuth(limiter, wide, host) {
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

// allowAuth spends the network's budget, then the /48's. A /48 refusal gives the
// network back its attempt, as registration does: the attempt never happened.
func allowAuth(limiter, wide *ratelimit.SlidingWindow, host string) bool {
	key := ratelimit.NetKey(host)
	if !limiter.Allow(key) {
		return false
	}
	if !wide.Allow(ratelimit.WideKey(host)) {
		limiter.Refund(key)
		return false
	}
	return true
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
			func() (bool, func()) { return allowRegistration(traceCtx, registerBudget, s) })
		if err != nil {
			reg.failSessionf(s, "auth_failed", err, "%v\n", err)
			return nil
		}
		player := lobby.NewPlayer(user)
		// Built before the slot is claimed: a panic in here would otherwise strand a
		// tracker slot that nothing releases - and that account cannot connect again
		// until a restart.
		model := tui.New(tui.Deps{
			SessionCtx:   traceCtx,
			Player:       player,
			Profiles:     deps.Profiles,
			Leaderboard:  deps.Leaderboard,
			LobbyManager: deps.LobbyManager,
			Games:        deps.Games,
		})
		// The connection, not just the session: closing a channel leaves the socket and
		// any other channel on it up until the peer notices. The session is for a
		// newer session on that same connection, which must not be hung up with it.
		conn, _ := s.Context().Value(ssh.ContextKeyConn).(gossh.Conn)
		st.run, st.endRun = context.WithCancel(context.Background())
		gen, err := deps.Tracker.Connect(player.UserID, conn, displaceable(s, st.endRun))
		if err != nil { // errServerFull, Connect's only refusal
			model.Close()
			reg.failSessionf(s, "rejected_full", err,
				"The server is full right now - please try again in a few minutes.\n")
			return nil
		}
		observability.SSHSession(traceCtx, "accepted")
		// Published before ResumeSeat: a panic there still has to ReleaseWith, and
		// releaseSession skips a session whose player was never stored. Only once the
		// slot is ours, though — a refused session must not cancel the grace timer
		// holding this player's seat, and a displaced one has finished its teardown
		// by now, so any timer it armed is there to cancel.
		st.player = player
		st.gen = gen
		st.model = model
		tui.ResumeSeat(model)

		// bubbletea's own recover prints to stderr and knows nothing about the span
		// or the metric, so reportingModel catches Init/Update/View first. Its
		// catching stays enabled all the same: it is the only thing covering the
		// goroutines bubbletea spawns per Cmd, and an unrecovered panic there takes
		// down the process for every connected player, not just this session.
		return reportingModel{Model: model, session: s, reg: reg}
	}
}

// closerFunc is an io.Closer made of a func.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// displaceable is what Connect closes when a newer session for the account arrives
// on s's own connection. Closing the channel alone ends nothing: wish quits the
// program only when the session's context ends, which charm ssh shares across the
// connection, and the channel cap frees its slot as soon as the channel closes, so
// one connection looping shells would hold any number of TUIs. Cancelling the run context first also covers a program not yet started,
// whose Run then returns at once.
func displaceable(s ssh.Session, endRun context.CancelFunc) io.Closer {
	return closerFunc(func() error {
		endRun()
		return s.Close()
	})
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
		// the filter, so it is granted here as well as there.
		size, moved := st.size.start(&reg.sizes, termSize{pty.Window.Width, pty.Window.Height})
		program := tea.NewProgram(model, append(bm.MakeOptions(s),
			tea.WithInput(st.input),
			tea.WithWindowSize(size.w, size.h),
			tea.WithFPS(sessionFPS),
			tea.WithFilter(st.filter),
			tea.WithContext(st.run))...)
		st.program.Store(program)
		if moved {
			reg.retier()
		}
		return program
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

// retier nudges every session the budget now sizes differently to take its new
// size: after a tier move, or after a leave freed extra cells.
// Each nudge is a Send on a goroutine of its own, because a Send waits for that
// session's event loop, and one whose peer stopped taking output is stuck: waited
// on in turn, it held up every session after it, sweep after sweep. A session has
// at most one nudge in flight (sessionSize.nudging), so a stuck one parks one
// goroutine, not one per sweep, until its program ends. Overlapping sweeps are
// harmless, as each nudge is granted against the tier as it is when the session
// reads it.
func (reg *sessionRegistry) retier() {
	reg.states.Range(func(_, v any) bool {
		st, _ := v.(*sessionState)
		// A program is stored only once its size has started. Send returns when the
		// program ends: wish runs every program activeterm lets through, and a run
		// cancels the program's context however it ends.
		if p := st.program.Load(); p != nil && st.size.stale() && st.size.nudging.CompareAndSwap(false, true) {
			go p.Send(retierMsg{})
		}
		return true
	})
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
//
// It also refuses a window-change before the shell or exec. charm ssh puts each one
// on a one-slot channel with a blocking send from the goroutine reading this stream,
// the pty-req already filled that slot, and nothing reads it before the session
// handler runs: a second resize stopped the stream for good, the connection's
// request mux backed up behind it, and the connection was never read again - no
// idle timeout, no teardown, and its listener slot gone until a restart. The pty-req
// carries the size, so nothing is lost. From the handler on, sessionLifecycle drains
// the slot. A subsystem does not count as a start: none is registered, so charm ssh
// refuses it and no handler ever drains.
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
		count, size, started := 0, 0, false
		for req := range reqs {
			switch req.Type {
			case "env":
				count++
				size += len(req.Payload)
				if count > maxEnvRequests || size > maxEnvBytes {
					_ = req.Reply(false, nil)
					continue
				}
			case "window-change":
				if !started {
					_ = req.Reply(false, nil)
					continue
				}
			case "shell", "exec":
				started = true
			}
			out <- req
		}
	}()
	return ch, out, nil
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
