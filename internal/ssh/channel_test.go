package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// startInProcessServer runs the real NewServer over a loopback listener, with a
// user repository that answers every fingerprint with user.
func startInProcessServer(t *testing.T, user *db.User) (string, *SessionTracker) {
	t.Helper()
	return startServerWith(t, stubUserRepo{user: user})
}

func startServerWith(tb testing.TB, repo db.Authenticator) (string, *SessionTracker) {
	tb.Helper()
	return startServerWithRegistry(tb, repo, newSessionRegistry(cellBudget, extraCellBudget))
}

// startServerWithRegistry is startServerWith over a registry the test sized.
func startServerWithRegistry(tb testing.TB, repo db.Authenticator, reg *sessionRegistry) (string, *SessionTracker) {
	tb.Helper()
	deps := newSessionDeps(tb, repo)
	deps.Config = &config.Config{
		SSHKeyPath:      tb.TempDir() + "/id_ed25519",
		RateLimitCount:  100,
		RateLimitWindow: time.Minute,
	}
	deps.Tracker = NewSessionTracker(0)
	server, err := newServer(deps, reg)
	require.NoError(tb, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	tb.Cleanup(func() {
		_ = server.Close()
		<-served
	})
	return listener.Addr().String(), deps.Tracker
}

func dialInProcess(tb testing.TB, addr, user string) *gossh.Client {
	tb.Helper()
	signer, err := gossh.NewSignerFromKey(testPrivateKey(tb))
	require.NoError(tb, err)
	client, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = client.Close() })
	return client
}

// A channel that never asks for a shell never reaches the middleware, so a cap
// enforced there bounds nothing: one connection could hold channels open without
// limit, each with its own request goroutine and buffers.
func TestNewServer_CapsSessionChannelsBeforeAccept(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(1), Username: "flooder"})
	client := dialInProcess(t, addr, "flooder")

	for range maxSessionsPerConnection {
		s, err := client.NewSession()
		require.NoError(t, err, "the cap refused a channel it should have admitted")
		t.Cleanup(func() { _ = s.Close() })
	}

	_, err := client.NewSession()
	var open *gossh.OpenChannelError
	require.ErrorAs(t, err, &open, "a channel over the cap was accepted")
	assert.Equal(t, gossh.ResourceShortage, open.Reason)
}

// Closing a channel has to give its slot back, or a client that reconnects its
// session channel a few times locks itself out of its own connection.
func TestNewServer_ClosedChannelFreesItsSlot(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(2), Username: "cycler"})
	client := dialInProcess(t, addr, "cycler")

	for range 3 * maxSessionsPerConnection {
		require.Eventually(t, func() bool {
			s, err := client.NewSession()
			if err != nil {
				return false
			}
			_ = s.Close()
			return true
		}, 2*time.Second, 10*time.Millisecond, "a closed channel kept its slot")
	}
}

// Every accepted env request is appended to the session for its whole life, so an
// unbounded stream of them is unbounded memory from one channel.
func TestNewServer_CapsEnvRequests(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(3), Username: "envy"})
	client := dialInProcess(t, addr, "envy")

	s, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	accepted := 0
	for range 1000 {
		if s.Setenv("K", "v") == nil {
			accepted++
		}
	}
	assert.Equal(t, maxEnvRequests, accepted)

	big, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = big.Close() })
	value := string(make([]byte, 1024))
	stored := 0
	for range maxEnvRequests {
		if big.Setenv("K", value) == nil {
			stored += len(value)
		}
	}
	assert.LessOrEqual(t, stored, maxEnvBytes, "the byte budget was not enforced")
	assert.Positive(t, stored, "an ordinary environment must still get through")
}

func testPrivateKey(tb testing.TB) ed25519.PrivateKey {
	tb.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(tb, err)
	return priv
}

func openShell(t *testing.T, client *gossh.Client) {
	t.Helper()
	s, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.RequestPty("xterm", 40, 80, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())
}

// Displacement has to hang up on the old connection, not just its channel: closing
// the channel leaves the TCP connection, and any other channel on it, open until the
// peer goes away on its own.
func TestNewServer_DisplacementClosesTheOldConnection(t *testing.T) {
	t.Parallel()
	user := &db.User{ID: testutil.UID(4), Username: "twice"}
	addr, tracker := startInProcessServer(t, user)

	first := dialInProcess(t, addr, "twice")
	openShell(t, first)
	gone := make(chan struct{})
	go func() {
		_ = first.Wait()
		close(gone)
	}()
	require.Eventually(t, func() bool { return tracker.Count() == 1 },
		2*time.Second, 10*time.Millisecond, "the first session never reached the tracker")

	openShell(t, dialInProcess(t, addr, "twice"))

	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("the displaced connection is still open")
	}
}

type unopenableChannel struct{ gossh.NewChannel }

var errUnopenable = errors.New("peer went away")

func (unopenableChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	return nil, nil, errUnopenable
}

// A channel that never opened has no request stream to filter: the caller gets the
// cause and nothing to read from, and no filtering goroutine is left behind.
func TestEnvCappedChannel_AcceptFailureIsReturned(t *testing.T) {
	t.Parallel()

	ch, reqs, err := envCappedChannel{NewChannel: unopenableChannel{}}.Accept()

	require.ErrorIs(t, err, errUnopenable)
	assert.Nil(t, ch)
	assert.Nil(t, reqs)
}

func ptyReqPayload(w, h uint32) []byte {
	return gossh.Marshal(struct {
		Term                    string
		Columns, Rows, PxW, PxH uint32
		Modes                   string
	}{"xterm", w, h, 0, 0, "\x00"})
}

func windowChangePayload(w, h uint32) []byte {
	return gossh.Marshal(struct{ Columns, Rows, PxW, PxH uint32 }{w, h, 0, 0})
}

// openRawSession opens a session channel with a pty and nothing else, so the test
// sends every request that follows itself.
func openRawSession(t *testing.T, client *gossh.Client) gossh.Channel {
	t.Helper()
	ch, reqs, err := client.OpenChannel("session", nil)
	require.NoError(t, err)
	go gossh.DiscardRequests(reqs)
	t.Cleanup(func() { _ = ch.Close() })
	ok, err := ch.SendRequest("pty-req", true, ptyReqPayload(80, 40))
	require.NoError(t, err)
	require.True(t, ok)
	return ch
}

func sendWindowChanges(ch gossh.Channel, n int) {
	for i := range n {
		_, _ = ch.SendRequest("window-change", false, windowChangePayload(uint32(81+i), 40))
	}
}

// within fails the test unless fn returns inside two seconds.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s hung: the connection is wedged", what)
	}
}

// openAnotherChannel is the probe for a wedged connection: the request mux that
// would answer it is the one a blocked request stream backs up.
func openAnotherChannel(client *gossh.Client) {
	ch, reqs, err := client.OpenChannel("session", nil)
	if err == nil {
		go gossh.DiscardRequests(reqs)
		_ = ch.Close()
	}
}

// charm ssh hands each window-change to a one-slot channel with a blocking send from
// the goroutine reading the channel's requests, and the pty-req already filled the
// slot. Before the shell nothing reads it, so a second resize stopped that goroutine
// for good, the connection's request mux backed up behind it, and the connection was
// never read again: no idle timeout, no teardown, and a listener slot gone for good.
func TestNewServer_WindowChangeBeforeShellDoesNotWedge(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(91), Username: "wedge"})
	client := dialInProcess(t, addr, "wedge")
	ch := openRawSession(t, client)

	sendWindowChanges(ch, 40)

	within(t, "the shell reply", func() { _, _ = ch.SendRequest("shell", true, nil) })
	within(t, "a second channel", func() { openAnotherChannel(client) })
}

// heldRepo refuses every lookup, but only once the test lets it: until then the
// session handler is parked inside sessionModel, past every read of the pty.
type heldRepo struct {
	stubUserRepo
	entered, release chan struct{}
}

func (r heldRepo) LoadUserByFingerprint(ctx context.Context, fp string) (*db.User, *db.PublicSSHKey, error) {
	close(r.entered)
	<-r.release
	return r.stubUserRepo.LoadUserByFingerprint(ctx, fp)
}

// A refused session's handler returns while its request stream is still open, and
// nothing read the window changes it got on the way to the refusal.
func TestNewServer_WindowChangeDuringRefusalDoesNotWedge(t *testing.T) {
	t.Parallel()
	repo := heldRepo{
		err:     errors.New("db down"),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	addr, _ := startServerWith(t, repo)
	client := dialInProcess(t, addr, "refused")
	ch := openRawSession(t, client)

	ok, err := ch.SendRequest("shell", true, nil)
	require.NoError(t, err)
	require.True(t, ok)
	<-repo.entered
	sendWindowChanges(ch, 40)
	close(repo.release)

	within(t, "a second channel", func() { openAnotherChannel(client) })
}

// OpenSSH's ControlMaster runs a second `ssh` as a second session channel on the
// connection the first one opened. Displacing the first session hung up that shared
// connection, and with it the session that had just arrived.
func TestNewServer_SecondChannelOnTheSameConnectionDisplacesOnlyTheFirst(t *testing.T) {
	t.Parallel()
	addr, tracker := startInProcessServer(t, &db.User{ID: testutil.UID(94), Username: "muxer"})
	client := dialInProcess(t, addr, "muxer")
	connGone := make(chan error, 1)
	go func() { connGone <- client.Wait() }()

	first, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	require.NoError(t, first.RequestPty("xterm", 40, 80, gossh.TerminalModes{}))
	require.NoError(t, first.Shell())
	require.Eventually(t, func() bool { return tracker.Count() == 1 }, 3*time.Second, 10*time.Millisecond)

	openShell(t, client)

	within(t, "the displaced session's end", func() { _ = first.Wait() })
	select {
	case err := <-connGone:
		t.Fatalf("the shared connection closed with the displaced session: %v", err)
	case <-time.After(time.Second):
	}
	assert.Equal(t, 1, tracker.Count())
}
