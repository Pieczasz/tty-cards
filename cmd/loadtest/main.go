// Command loadtest opens N concurrent SSH sessions against a running tty-cards
// server and reports connect, first-frame and in-game latency percentiles. A share
// of the sessions can play Crazy Eights through the real TUI; the rest park in a menu.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// config is one run's flags.
type config struct {
	addr, journey, prefix string
	sessions, tableSize   int
	hold, ramp            time.Duration
	playFraction          float64
	pace                  pace
	menuSize              termSize
}

// termSize is a PTY size. Players stay at 80x24, the size their screen reader is
// built for; menu sessions can be larger, because a big terminal is what costs the
// server memory (its renderer holds a buffer per cell, up to the 120x40 clamp).
type termSize struct{ cols, rows int }

func (t *termSize) String() string { return fmt.Sprintf("%dx%d", t.cols, t.rows) }

func (t *termSize) Set(v string) error {
	if _, err := fmt.Sscanf(v, "%dx%d", &t.cols, &t.rows); err != nil || t.cols < 20 || t.rows < 10 {
		return fmt.Errorf("want COLSxROWS of at least 20x10, got %q", v)
	}
	return nil
}

func parseFlags() (config, error) {
	c := config{menuSize: termSize{termWidth, termHeight}}
	flag.StringVar(&c.addr, "addr", "127.0.0.1:6969", "server address")
	flag.IntVar(&c.sessions, "sessions", 200, "concurrent sessions to open")
	flag.DurationVar(&c.hold, "hold", 60*time.Second, "how long to hold sessions open after ramp")
	flag.DurationVar(&c.ramp, "ramp", 10*time.Second, "spread connects over this window")
	flag.StringVar(&c.journey, "journey", "menu", "what non-playing sessions do: menu (park in the join-lobby browser) or idle")
	flag.StringVar(&c.prefix, "prefix", "load", "username prefix, <=11 chars of [A-Za-z0-9_]")
	flag.Float64Var(&c.playFraction, "play-fraction", 0, "share of sessions that play Crazy Eights, rounded down to whole tables")
	flag.IntVar(&c.tableSize, "table-size", 4, "seats per table, 2-6")
	flag.DurationVar(&c.pace.thinkMin, "think-min", time.Second, "shortest pause before a move")
	flag.DurationVar(&c.pace.thinkMax, "think-max", 4*time.Second, "longest pause before a move")
	flag.DurationVar(&c.pace.menu, "key-delay", 500*time.Millisecond, "mean pause between menu keys")
	flag.Var(&c.menuSize, "menu-size", "PTY size of non-playing sessions, COLSxROWS")
	flag.Parse()

	switch {
	case c.journey != "menu" && c.journey != "idle":
		return c, fmt.Errorf("unknown journey %q", c.journey)
	case len(c.prefix) > 11:
		return c, errors.New("prefix must be 11 characters or fewer")
	case c.playFraction < 0 || c.playFraction > 1:
		return c, errors.New("play-fraction must be between 0 and 1")
	case c.tableSize < 2 || c.tableSize > 6:
		return c, errors.New("table-size must be between 2 and 6, the Crazy Eights seat range")
	case c.pace.thinkMin > c.pace.thinkMax:
		return c, errors.New("think-min must not exceed think-max")
	case c.pace.thinkMax >= 25*time.Second:
		return c, errors.New("think-max must stay under the server's 30s turn clock")
	}
	return c, nil
}

// defaultAuthRate is the server's RATE_LIMIT_CONNECTIONS per RATE_LIMIT_WINDOW default.
const defaultAuthRate = 5.0

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if rate := float64(cfg.sessions) / max(cfg.ramp.Seconds(), 1e-9); rate > defaultAuthRate {
		fmt.Fprintf(os.Stderr, "note: the ramp opens %.1f sessions/s from one network; a server on the default "+
			"RATE_LIMIT_CONNECTIONS=5 per RATE_LIMIT_WINDOW=1s refuses the excess at auth. Raise it on the server "+
			"under test, or use -ramp %s or longer.\n", rate, (time.Duration(float64(cfg.sessions)/defaultAuthRate) * time.Second))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	r := &results{addr: cfg.addr, journey: cfg.journey, prefix: cfg.prefix, menuSize: cfg.menuSize}
	roles := planRoles(cfg.sessions, cfg.playFraction, cfg.tableSize, cfg.pace)
	release := make(chan struct{})

	var wg sync.WaitGroup
	gap := time.Duration(0)
	if cfg.sessions > 1 {
		gap = cfg.ramp / time.Duration(cfg.sessions-1)
	}
	started := time.Now()
	for i, role := range roles {
		select {
		case <-stop:
			fmt.Fprintln(os.Stderr, "interrupted during ramp")
			close(release)
			wg.Wait()
			r.report(cfg.sessions, time.Since(started))
			return
		case <-time.After(gap):
		}
		wg.Go(func() {
			r.client(i, role, release)
		})
	}
	rampDone := time.Since(started)
	fmt.Fprintf(os.Stderr, "ramp complete in %v; holding %v\n", rampDone.Round(time.Millisecond), cfg.hold)

	select {
	case <-stop:
		fmt.Fprintln(os.Stderr, "interrupted during hold")
	case <-time.After(cfg.hold):
	}
	close(release)
	wg.Wait()
	r.report(cfg.sessions, time.Since(started))
}

// role is what one session does: play a seat at a table, or the run's journey.
type role struct {
	table *table // nil: not a player
	host  bool
	seats int
	pace  pace
}

// planRoles spreads the players through the ramp rather than connecting them all
// first, and seats them in connect order, so each table's host connects before its
// guests. Players are rounded down to whole tables.
func planRoles(sessions int, fraction float64, seats int, p pace) []role {
	players := int(float64(sessions)*fraction) / seats * seats
	roles := make([]role, sessions)
	var t *table
	seated := 0
	for i := range roles {
		if (i+1)*players/sessions == i*players/sessions {
			continue
		}
		if seated%seats == 0 {
			t = newTable()
		}
		roles[i] = role{table: t, host: seated%seats == 0, seats: seats, pace: p}
		seated++
	}
	return roles
}

// results is one run's configuration and everything its clients report back.
type results struct {
	addr    string
	journey string
	prefix  string

	mu        sync.Mutex
	connectMs []float64
	frameMs   []float64
	moveMs    []float64
	frameRate map[string][]float64 // output bursts per second per session, by journey
	bytes     int64
	ok        int
	moves     int
	unapplied int
	games     int
	errs      map[string]int

	menuSize termSize

	live atomic.Int64
	peak atomic.Int64
}

func (r *results) record(journey string, connect time.Duration, res drainResult, held time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectMs = append(r.connectMs, ms(connect))
	if res.firstFrame > 0 {
		r.frameMs = append(r.frameMs, ms(res.firstFrame))
	}
	if held > 0 {
		if r.frameRate == nil {
			r.frameRate = map[string][]float64{}
		}
		r.frameRate[journey] = append(r.frameRate[journey], float64(res.reads)/held.Seconds())
	}
	r.bytes += res.bytes
	r.ok++
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// fail counts err, which names the stage it happened at, among the run's errors.
func (r *results) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errs == nil {
		r.errs = map[string]int{}
	}
	r.errs[err.Error()]++
}

func (r *results) moved(rtt time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moves++
	r.moveMs = append(r.moveMs, ms(rtt))
}

func (r *results) notApplied() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unapplied++
}

func (r *results) gameOver() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.games++
}

// session is one client's open connection and its PTY session.
type session struct {
	*ssh.Session
	conn    *ssh.Client
	connect time.Duration
}

func (s *session) close() {
	_ = s.Close()
	_ = s.conn.Close()
}

// The refusals worth telling apart, each with what to change on the server under test.
const (
	errAuthRefused = "dial/auth: refused at auth - the server accepts any key, so this is its per-network " +
		"limit (raise RATE_LIMIT_CONNECTIONS or lengthen -ramp)"
	errDialTimeout = "dial/auth: timed out - the listener stops accepting past 2*MAX_CONNECTIONS " +
		"sockets, or the server is overloaded"
	errRegistration = "session refused: registration budget spent (raise REGISTRATION_LIMIT, REGISTRATION_LIMIT_WIDE " +
		"and REGISTRATION_GLOBAL_LIMIT past -sessions on the server under test)"
	errNameTaken  = "session refused: username taken (use a new -prefix per run on the same database)"
	errServerFull = "session refused: server full (MAX_CONNECTIONS sessions already open)"
)

// dialError names why a connection never got a session.
func dialError(err error) error {
	var netErr net.Error
	switch {
	case strings.Contains(err.Error(), "unable to authenticate"):
		return errors.New(errAuthRefused)
	case errors.As(err, &netErr) && netErr.Timeout():
		return errors.New(errDialTimeout)
	}
	return fmt.Errorf("dial/auth: %w", err)
}

// endedError names why the server closed a session before the hold ended, from the
// message it wrote to stderr on the way out.
func endedError(stderr string) error {
	switch {
	case strings.Contains(stderr, "too many new accounts"):
		return errors.New(errRegistration)
	case strings.Contains(stderr, "could not register that name"):
		return errors.New(errNameTaken)
	case strings.Contains(stderr, "server is full"):
		return errors.New(errServerFull)
	case strings.TrimSpace(stderr) == "":
		return errors.New("session ended by the server before the hold ended, no message")
	}
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	return errors.New("session ended by the server: " + line)
}

// openSession dials, authenticates and requests a PTY. The error names the stage
// that failed; on success the caller owns the session and closes it.
func (r *results) openSession(i int, size termSize) (*session, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keygen: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signer: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:            fmt.Sprintf("%s_%04d", r.prefix, i),
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // G106: a load tool against a server we just started
		Timeout:         30 * time.Second,
	}

	dialStart := time.Now()
	conn, err := ssh.Dial("tcp", r.addr, cfg)
	if err != nil {
		return nil, dialError(err)
	}
	connect := time.Since(dialStart)

	sess, err := conn.NewSession()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("new session: %w", err)
	}
	s := &session{Session: sess, conn: conn, connect: connect}

	if err := sess.RequestPty("xterm-256color", size.rows, size.cols, ssh.TerminalModes{
		ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		s.close()
		return nil, fmt.Errorf("request pty: %w", err)
	}
	return s, nil
}

// termWidth x termHeight is what players use: the commonest terminal, and too short
// for card art, so a hand is the rank-and-suit strip a bot can read.
const termWidth, termHeight = 80, 24

// client runs one session end to end. It returns only after release closes, so the
// caller's WaitGroup measures the true concurrency window.
func (r *results) client(i int, role role, release <-chan struct{}) {
	size := r.menuSize
	if role.table != nil || size == (termSize{}) {
		size = termSize{termWidth, termHeight}
	}
	sess, err := r.openSession(i, size)
	if err != nil {
		r.fail(err)
		return
	}
	defer sess.close()
	sessionStart := time.Now()

	stdout, stdin, stderr, err := startShell(sess)
	if err != nil {
		r.fail(err)
		return
	}

	live := r.live.Add(1)
	for {
		peak := r.peak.Load()
		if live <= peak || r.peak.CompareAndSwap(peak, live) {
			break
		}
	}
	defer r.live.Add(-1)

	stop := make(chan struct{})
	var actors sync.WaitGroup
	defer actors.Wait()
	defer close(stop)
	journey, sink := r.startActor(&actors, role, stdin, stop)

	drained := make(chan drainResult, 1)
	go drain(stdout, sessionStart, sink, drained)

	select {
	case <-drained:
		// The server hung up first. Whatever it said on the way out is the cause, and
		// stdout reaching EOF does not mean the ssh package has finished copying stderr:
		// read too soon, a refusal reads as a silent hang-up.
		waitExit(sess)
		r.fail(endedError(stderr.String()))
		return
	case <-release:
	}
	// Closing the channel is what a real client hanging up looks like; the read
	// goroutine then returns EOF and reports what it saw.
	_ = sess.Close()
	res := <-drained
	// A transport error and a success are exclusive: counting both put the same
	// session in the succeeded and the failed line, and folded its latency into the
	// percentiles - reading best exactly when the server is dropping connections,
	// which is the run this tool exists to measure.
	if res.err != nil && !errors.Is(res.err, io.EOF) {
		r.fail(fmt.Errorf("read: %w", res.err))
		return
	}
	r.record(journey, sess.connect, res, time.Since(sessionStart))
}

// startShell wires the session's pipes and starts the shell, which is the TUI.
func startShell(sess *session) (io.Reader, io.WriteCloser, *syncBuffer, error) {
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stderr := &syncBuffer{}
	sess.Stderr = stderr
	if err := sess.Shell(); err != nil {
		return nil, nil, nil, fmt.Errorf("shell: %w", err)
	}
	return stdout, stdin, stderr, nil
}

// startActor starts whatever types into the session for its role and returns the
// journey it reports under and where the session's output goes. Only a player reads
// the screen, so only a player pays for emulating one.
func (r *results) startActor(wg *sync.WaitGroup, role role, stdin io.WriteCloser, stop <-chan struct{}) (string, io.Writer) {
	switch {
	case role.table != nil:
		scr := newScreen(termWidth, termHeight)
		b := &bot{host: role.host, seats: role.seats, table: role.table, scr: scr, keys: stdin, pace: role.pace, stats: r, stop: stop}
		wg.Go(b.run)
		return "play", scr
	case r.journey == "menu":
		wg.Go(func() { keystrokes(stdin, stop) })
	}
	return r.journey, io.Discard
}

// waitExit waits, briefly, for the session's exit and its stderr to be copied.
func waitExit(sess *session) {
	done := make(chan struct{})
	go func() {
		_ = sess.Wait() // the exit status is not the cause; stderr is
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// syncBuffer collects what the server writes to stderr; the ssh package copies it on
// its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < 4096 {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type drainResult struct {
	bytes      int64
	reads      int
	firstFrame time.Duration
	err        error
}

// drain reads until EOF into sink, timing the first byte of PTY output as the first
// frame. Each read is counted: the renderer flushes a frame in one write, so reads
// per second approximate the frame rate the server pushes.
func drain(out io.Reader, since time.Time, sink io.Writer, done chan<- drainResult) {
	buf := make([]byte, 32*1024)
	var res drainResult
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if res.firstFrame == 0 {
				res.firstFrame = time.Since(since)
			}
			res.bytes += int64(n)
			res.reads++
			_, _ = sink.Write(buf[:n])
		}
		if err != nil {
			res.err = err
			done <- res
			return
		}
	}
}

// menuNudge is how often a parked menu session moves its cursor. The server drops a
// session that sends nothing for five minutes, and a hold longer than that would
// otherwise measure the idle reaper rather than parked players.
const menuNudge = 60 * time.Second

// keystrokes walks home -> join-lobby browser and parks there, which is the hot
// spot: the browser re-renders on a 2s tick plus a cursor blink for as long as a
// player is looking at it. "f" is the home view's key for the join browser.
func keystrokes(in io.WriteCloser, stop <-chan struct{}) {
	defer func() { _ = in.Close() }()
	steps := []struct {
		wait time.Duration
		key  string
	}{
		{2 * time.Second, "f"},
		{1500 * time.Millisecond, keyDown}, // move the browser cursor once
	}
	for _, s := range steps {
		select {
		case <-stop:
			return
		case <-time.After(s.wait):
		}
		if _, err := io.WriteString(in, s.key); err != nil {
			return
		}
	}
	for keys := []string{keyUp, keyDown}; ; keys[0], keys[1] = keys[1], keys[0] {
		select {
		case <-stop:
			return
		case <-time.After(menuNudge):
		}
		if _, err := io.WriteString(in, keys[0]); err != nil {
			return
		}
	}
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p / 100 * float64(len(sorted)-1))
	return sorted[i]
}

func (r *results) report(requested int, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	failed := 0
	for _, n := range r.errs {
		failed += n
	}

	fmt.Printf("\n=== loadtest %s journey=%s sessions=%d ===\n", r.addr, r.journey, requested)
	fmt.Printf("wall clock        %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("succeeded         %d\n", r.ok)
	fmt.Printf("failed            %d\n", failed)
	fmt.Printf("  rate-limited    %d\n", r.errs[errAuthRefused])
	fmt.Printf("  registration    %d\n", r.errs[errRegistration]+r.errs[errNameTaken])
	fmt.Printf("peak concurrent   %d\n", r.peak.Load())
	fmt.Printf("bytes received    %d (%.1f KiB/session)\n", r.bytes, float64(r.bytes)/1024/float64(max(r.ok, 1)))
	fmt.Printf("moves made        %d (%d not applied within %v)\n", r.moves, r.unapplied, settleTimeout)
	fmt.Printf("games completed   %d\n", r.games)

	type metric struct {
		name string
		v    []float64
		unit string
	}
	rows := make([]metric, 0, 3+len(r.frameRate))
	rows = append(rows,
		metric{"connect+auth", r.connectMs, "ms"},
		metric{"time-to-first-frame", r.frameMs, "ms"},
		metric{"move round-trip", r.moveMs, "ms"},
	)
	for _, j := range slices.Sorted(maps.Keys(r.frameRate)) {
		rows = append(rows, metric{"frames/s " + j, r.frameRate[j], "/s"})
	}
	fmt.Printf("\n%-20s %8s %10s %10s %10s\n", "metric", "n", "p50", "p95", "max")
	for _, m := range rows {
		slices.Sort(m.v)
		maxV := 0.0
		if len(m.v) > 0 {
			maxV = m.v[len(m.v)-1]
		}
		fmt.Printf("%-20s %8d %8.1f%-2s %8.1f%-2s %8.1f%-2s\n",
			m.name, len(m.v), pct(m.v, 50), m.unit, pct(m.v, 95), m.unit, maxV, m.unit)
	}
	if len(r.errs) > 0 {
		fmt.Printf("\nerrors:\n")
		for _, msg := range slices.Sorted(maps.Keys(r.errs)) {
			fmt.Printf("  %6d  %s\n", r.errs[msg], msg)
		}
	}
	fmt.Println()
}
