package ssh

import (
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
	return startServerWithRegistry(tb, repo, &sessionRegistry{})
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
