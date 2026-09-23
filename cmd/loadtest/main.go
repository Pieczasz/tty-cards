// Command loadtest opens N concurrent SSH sessions against a running
// terminal-card server and reports connect/first-frame latency percentiles.
//
// It is a measurement tool, not a test: it never asserts, it prints numbers.
// Every client gets its own ephemeral ed25519 key, so it also registers its own
// account on first connect - which is what keeps the one-session-per-account
// tracker and the per-connection channel cap out of the way. It also means the
// server under test needs REGISTRATION_LIMIT raised past -sessions: the default
// budget is 5 new accounts per network per hour.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6969", "server address")
	sessions := flag.Int("sessions", 200, "concurrent sessions to open")
	hold := flag.Duration("hold", 60*time.Second, "how long to hold sessions open after ramp")
	ramp := flag.Duration("ramp", 10*time.Second, "spread connects over this window")
	journey := flag.String("journey", "menu", "menu (park in the join-lobby browser) or idle")
	// Usernames are unique per account and accounts outlive the run, so a second
	// run against the same database needs a fresh prefix or every registration
	// fails with "username taken".
	prefix := flag.String("prefix", "load", "username prefix, <=11 chars of [A-Za-z0-9_]")
	flag.Parse()

	if *journey != "menu" && *journey != "idle" {
		fmt.Fprintf(os.Stderr, "unknown journey %q\n", *journey)
		os.Exit(2)
	}
	if len(*prefix) > 11 {
		fmt.Fprintln(os.Stderr, "prefix must be 11 characters or fewer")
		os.Exit(2)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	r := &results{addr: *addr, journey: *journey, prefix: *prefix}
	release := make(chan struct{})

	var wg sync.WaitGroup
	gap := time.Duration(0)
	if *sessions > 1 {
		gap = *ramp / time.Duration(*sessions-1)
	}
	started := time.Now()
	for i := range *sessions {
		select {
		case <-stop:
			fmt.Fprintln(os.Stderr, "interrupted during ramp")
			close(release)
			wg.Wait()
			r.report(*sessions, time.Since(started))
			return
		case <-time.After(gap):
		}
		wg.Go(func() {
			r.client(i, release)
		})
	}
	rampDone := time.Since(started)
	fmt.Fprintf(os.Stderr, "ramp complete in %v; holding %v\n", rampDone.Round(time.Millisecond), *hold)

	select {
	case <-stop:
		fmt.Fprintln(os.Stderr, "interrupted during hold")
	case <-time.After(*hold):
	}
	close(release)
	wg.Wait()
	r.report(*sessions, time.Since(started))
}

// results is one run's configuration and everything its clients report back.
type results struct {
	addr    string
	journey string
	prefix  string

	mu        sync.Mutex
	connectMs []float64
	frameMs   []float64
	bytes     int64
	ok        int
	errs      map[string]int

	live atomic.Int64
	peak atomic.Int64
}

func (r *results) record(connect, frame time.Duration, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectMs = append(r.connectMs, float64(connect)/float64(time.Millisecond))
	if frame > 0 {
		r.frameMs = append(r.frameMs, float64(frame)/float64(time.Millisecond))
	}
	r.bytes += n
	r.ok++
}

// fail counts err, which names the stage it happened at, among the run's errors.
func (r *results) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.errs == nil {
		r.errs = map[string]int{}
	}
	r.errs[err.Error()]++
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

// openSession dials, authenticates and requests a PTY. The error names the stage
// that failed; on success the caller owns the session and closes it.
func (r *results) openSession(i int) (*session, error) {
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
		return nil, fmt.Errorf("dial/auth: %w", err)
	}
	connect := time.Since(dialStart)

	sess, err := conn.NewSession()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("new session: %w", err)
	}
	s := &session{Session: sess, conn: conn, connect: connect}

	if err := sess.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{
		ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		s.close()
		return nil, fmt.Errorf("request pty: %w", err)
	}
	return s, nil
}

// client runs one session end to end. It returns only after release closes, so the
// caller's WaitGroup measures the true concurrency window.
func (r *results) client(i int, release <-chan struct{}) {
	sess, err := r.openSession(i)
	if err != nil {
		r.fail(err)
		return
	}
	defer sess.close()
	sessionStart := time.Now()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		r.fail(fmt.Errorf("stdout pipe: %w", err))
		return
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		r.fail(fmt.Errorf("stdin pipe: %w", err))
		return
	}

	if err := sess.Shell(); err != nil {
		r.fail(fmt.Errorf("shell: %w", err))
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

	drained := make(chan drainResult, 1)
	go drain(stdout, sessionStart, drained)

	if r.journey == "menu" {
		go r.keystrokes(stdin, release)
	}

	<-release
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
	r.record(sess.connect, res.firstFrame, res.bytes)
}

type drainResult struct {
	bytes      int64
	firstFrame time.Duration
	err        error
}

// drain reads until EOF, timing the first byte of PTY output as the first frame.
func drain(out io.Reader, since time.Time, done chan<- drainResult) {
	buf := make([]byte, 32*1024)
	var res drainResult
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if res.firstFrame == 0 {
				res.firstFrame = time.Since(since)
			}
			res.bytes += int64(n)
		}
		if err != nil {
			res.err = err
			done <- res
			return
		}
	}
}

// keystrokes walks home -> join-lobby browser and parks there, which is the hot
// spot: the browser re-renders on a 2s tick plus a cursor blink for as long as a
// player is looking at it. "f" is the home view's key for the join browser.
func (r *results) keystrokes(in io.WriteCloser, release <-chan struct{}) {
	defer func() { _ = in.Close() }()
	steps := []struct {
		wait time.Duration
		key  string
	}{
		{2 * time.Second, "f"},
		{1500 * time.Millisecond, "\x1b[B"}, // down: move the browser cursor once
	}
	for _, s := range steps {
		select {
		case <-release:
			return
		case <-time.After(s.wait):
		}
		if _, err := in.Write([]byte(s.key)); err != nil {
			return
		}
	}
	<-release
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
	slices.Sort(r.connectMs)
	slices.Sort(r.frameMs)

	failed := 0
	for _, n := range r.errs {
		failed += n
	}

	fmt.Printf("\n=== loadtest %s journey=%s sessions=%d ===\n", r.addr, r.journey, requested)
	fmt.Printf("wall clock        %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("succeeded         %d\n", r.ok)
	fmt.Printf("failed            %d\n", failed)
	fmt.Printf("peak concurrent   %d\n", r.peak.Load())
	fmt.Printf("bytes received    %d (%.1f KiB/session)\n", r.bytes, float64(r.bytes)/1024/float64(max(r.ok, 1)))
	fmt.Printf("\n%-18s %10s %10s %10s %10s\n", "metric", "n", "p50 ms", "p95 ms", "max ms")
	for _, m := range []struct {
		name string
		v    []float64
	}{
		{"connect+auth", r.connectMs},
		{"time-to-first-frame", r.frameMs},
	} {
		maxV := 0.0
		if len(m.v) > 0 {
			maxV = m.v[len(m.v)-1]
		}
		fmt.Printf("%-18s %10d %10.1f %10.1f %10.1f\n",
			m.name, len(m.v), pct(m.v, 50), pct(m.v, 95), maxV)
	}
	if len(r.errs) > 0 {
		fmt.Printf("\nerrors:\n")
		for _, msg := range slices.Sorted(maps.Keys(r.errs)) {
			fmt.Printf("  %6d  %s\n", r.errs[msg], msg)
		}
	}
	fmt.Println()
}
