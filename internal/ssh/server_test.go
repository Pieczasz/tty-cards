//go:build integration

package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cryptossh "golang.org/x/crypto/ssh"
)

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func generateSigner(t *testing.T) cryptossh.Signer {
	t.Helper()
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer, err := cryptossh.NewSignerFromKey(privKey)
	require.NoError(t, err)
	return signer
}

type testEnv struct {
	userRepo db.UserRepository
	addr     string
	cleanup  func()
}

func setupTestEnvironment(t *testing.T) testEnv {
	t.Helper()
	gormDB := testutil.SetupTestDB(t)
	userRepo := repository.NewUserRepository(gormDB)
	matchRepo := repository.NewMatchRepository(gormDB)

	listener := listenLocal(t)
	addr := listener.Addr().String()

	deps := ServerDependencies{
		Config: &config.Config{
			ServerPort:         listener.Addr().(*net.TCPAddr).Port,
			SSHKeyPath:         t.TempDir() + "/id_ed25519",
			RateLimitCount:     5,
			RateLimitWindow:    time.Second,
			RegistrationLimit:  5,
			RegistrationWindow: time.Hour,
		},
		UserRepository: userRepo,
		LobbyManager:   lobby.NewManager(context.Background(), matchRepo),
		GameRegistry:   game.NewRegistry(),
	}

	server, err := SetupServer(deps)
	require.NoError(t, err)

	go func() {
		_ = server.Serve(listener)
	}()

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond, "ssh server did not become ready")

	return testEnv{
		userRepo: userRepo,
		addr:     addr,
		cleanup:  func() { server.Close() },
	}
}

func TestServer_NewUserConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH integration test in short mode")
	}
	t.Parallel()

	env := setupTestEnvironment(t)
	defer env.cleanup()

	signer := generateSigner(t)
	clientConfig := &cryptossh.ClientConfig{
		User: "testuser_new",
		Auth: []cryptossh.AuthMethod{
			cryptossh.PublicKeys(signer),
		},
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
	}

	client, err := cryptossh.Dial("tcp", env.addr, clientConfig)
	require.NoError(t, err)

	session, err := client.NewSession()
	require.NoError(t, err)
	defer session.Close()

	err = session.RequestPty("xterm", 80, 40, cryptossh.TerminalModes{})
	require.NoError(t, err)
	err = session.Shell()
	require.NoError(t, err)

	var user *db.User
	var key *db.PublicKey
	require.Eventually(t, func() bool {
		user, key, _ = env.userRepo.LoadUserByFingerprint(context.Background(), cryptossh.FingerprintSHA256(signer.PublicKey()))
		return user != nil
	}, 2*time.Second, 50*time.Millisecond, "user was not registered")

	require.NotNil(t, user)
	require.NotNil(t, key)
	require.Equal(t, "testuser_new", user.Username)
}

func TestServer_SecondSessionDisplaces(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH integration test in short mode")
	}
	t.Parallel()

	env := setupTestEnvironment(t)
	defer env.cleanup()

	signer := generateSigner(t)
	clientConfig := &cryptossh.ClientConfig{
		User:            "testuser_dup",
		Auth:            []cryptossh.AuthMethod{cryptossh.PublicKeys(signer)},
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
	}

	client1, err := cryptossh.Dial("tcp", env.addr, clientConfig)
	require.NoError(t, err)
	defer client1.Close()
	session1, err := client1.NewSession()
	require.NoError(t, err)
	defer session1.Close()

	_ = session1.RequestPty("xterm", 80, 40, cryptossh.TerminalModes{})
	_ = session1.Shell()

	require.Eventually(t, func() bool {
		user, _, _ := env.userRepo.LoadUserByFingerprint(context.Background(), cryptossh.FingerprintSHA256(signer.PublicKey()))
		return user != nil
	}, 2*time.Second, 50*time.Millisecond)

	client2, err := cryptossh.Dial("tcp", env.addr, clientConfig)
	require.NoError(t, err)
	defer client2.Close()

	session2, err := client2.NewSession()
	require.NoError(t, err)
	defer session2.Close()

	_ = session2.RequestPty("xterm", 80, 40, cryptossh.TerminalModes{})
	err = session2.Shell()
	require.NoError(t, err, "a second session displaces the first instead of being refused")

	// Displacing has to hang up on the first session, not merely forget it: a zombie
	// keeps its TUI, its lobby subscription and a tracker slot alive until its TCP
	// dies. Wait returns once the server closes the channel.
	closed := make(chan error, 1)
	go func() { closed <- session1.Wait() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the displaced session is still open")
	}
}

func TestServer_ExistingUserConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH integration test in short mode")
	}
	t.Parallel()

	env := setupTestEnvironment(t)
	defer env.cleanup()

	signer := generateSigner(t)
	clientConfig := &cryptossh.ClientConfig{
		User:            "testuser_exist",
		Auth:            []cryptossh.AuthMethod{cryptossh.PublicKeys(signer)},
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
	}

	client1, err := cryptossh.Dial("tcp", env.addr, clientConfig)
	require.NoError(t, err)
	session1, err := client1.NewSession()
	require.NoError(t, err)
	_ = session1.RequestPty("xterm", 80, 40, cryptossh.TerminalModes{})
	_ = session1.Shell()

	require.Eventually(t, func() bool {
		user, _, _ := env.userRepo.LoadUserByFingerprint(context.Background(), cryptossh.FingerprintSHA256(signer.PublicKey()))
		return user != nil
	}, 2*time.Second, 50*time.Millisecond)

	session1.Close()
	client1.Close()

	require.Eventually(t, func() bool {
		client2, err := cryptossh.Dial("tcp", env.addr, clientConfig)
		if err != nil {
			return false
		}
		session2, err := client2.NewSession()
		if err != nil {
			client2.Close()
			return false
		}
		err = session2.Shell()
		session2.Close()
		client2.Close()
		return err == nil
	}, 2*time.Second, 50*time.Millisecond, "reconnect after disconnect failed")
}

func TestServer_RateLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSH integration test in short mode")
	}
	t.Parallel()

	env := setupTestEnvironment(t)
	defer env.cleanup()

	signer := generateSigner(t)
	clientConfig := &cryptossh.ClientConfig{
		User:            "testuser_limit",
		Auth:            []cryptossh.AuthMethod{cryptossh.PublicKeys(signer)},
		HostKeyCallback: cryptossh.InsecureIgnoreHostKey(),
	}

	var failed bool
	for range 6 {
		client, err := cryptossh.Dial("tcp", env.addr, clientConfig)
		if err != nil {
			failed = true
			break
		}
		session, err := client.NewSession()
		if err != nil {
			failed = true
			break
		}

		err = session.Shell()
		if err != nil {
			failed = true
			break
		}
		session.Close()
		client.Close()
	}

	assert.True(t, failed, "Expected rate limit to block connection")
}
