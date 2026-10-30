package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/observability"

	"charm.land/ssh"
	"charm.land/wish/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

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

func sessionLifecycle(deps Deps, reg *sessionRegistry) wish.Middleware {
	return func(sh ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			// First, before anything can refuse the session: from here to the end of
			// the channel's request stream, a resize must never wait.
			s = takeWindowChanges(s)
			// A handler charm ssh started just before Close can get here after the
			// drain stopped counting; it refuses itself rather than run untracked.
			if !reg.admit() {
				// Before any span: this session is never started, only counted.
				observability.SSHSession(s.Context(), "rejected_draining")
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
			defer reg.leaveSizes(s)

			sh(s)
		}
	}
}

// resizingSession is a session whose window changes come from takeWindowChanges.
type resizingSession struct {
	ssh.Session
	winch <-chan ssh.Window
}

func (s *resizingSession) Pty() (ssh.Pty, <-chan ssh.Window, bool) {
	pty, _, ok := s.Session.Pty()
	return pty, s.winch, ok
}

// takeWindowChanges takes the session's window changes off charm ssh as they arrive
// and keeps only the latest for whoever reads them. charm ssh hands each one over
// with a blocking send on a one-slot channel, from the goroutine reading the
// channel's requests. Its only reader, wish's bubbletea middleware, reads only while
// a program runs, and only as fast as that program's event loop takes messages - and
// the loop stops when the peer stops taking output. A refused session, one whose
// program ended, or one whose loop is stuck then stops its request stream at the
// second resize, the connection's request mux backs up behind it, and the connection
// is never read again: closing it ends nothing, so the session keeps its tracker
// slot and its listener slot until a restart.
//
// The goroutine ends when charm ssh closes the channel, which it does when the
// request stream ends. The latest-size channel is never closed: wish reads it in a
// loop, and a closed one would hand it zero sizes forever.
func takeWindowChanges(s ssh.Session) ssh.Session {
	_, in, _ := s.Pty()
	if in == nil { // no pty-req, so charm ssh refuses every resize itself
		return s
	}
	latest := make(chan ssh.Window, 1)
	go func() {
		for w := range in {
			// The only sender, so once the stale size is gone there is room.
			select {
			case <-latest:
			default:
			}
			latest <- w
		}
	}()
	return &resizingSession{Session: s, winch: latest}
}

func (reg *sessionRegistry) startSession(s ssh.Session) *sessionState {
	pty, _, _ := s.Pty()
	tracer := otel.Tracer("tty-cards/ssh")
	//nolint:spancheck // the span outlives this function: finishSession ends it; live.Done runs after that
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
	return st //nolint:spancheck // the span outlives this call: finishSession ends it; live.Done is the last defer
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

// leaveSizes gives the session's cells back. It runs after the program has ended, so
// held is no longer the event loop's. A session that held extra cells sweeps even
// when the tier stays put: another may be held below the tier for want of them, and
// would otherwise keep its smaller screen until it resized.
func (reg *sessionRegistry) leaveSizes(s ssh.Session) {
	st, ok := reg.load(s)
	if !ok || st.program.Load() == nil {
		return
	}
	if st.size.budget.leave(st.size.held) || st.size.held > 0 {
		reg.retier()
	}
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
