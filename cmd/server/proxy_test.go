package main

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendProxyHeader dials the listener and claims to be forwarding 203.0.113.7.
func sendProxyHeader(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = io.WriteString(conn, "PROXY TCP4 203.0.113.7 127.0.0.1 40000 6969\r\n")
	require.NoError(t, err)
	return conn
}

// The PROXY header names the address every limiter keys on, so honoring it from any
// peer lets any peer pick its own address. With trusted networks configured, a header
// from anywhere else has to cost the sender its connection.
func TestProxyListener_RefusesAHeaderFromOutsideTheTrustedNetworks(t *testing.T) {
	t.Parallel()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listener, err := proxyListener(inner, &config.Config{
		ProxyProtocol:     true,
		ProxyTrustedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
	}()

	conn := sendProxyHeader(t, inner.Addr().String())
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
	var netErr net.Error
	require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "the untrusted sender's connection was kept open")

	select {
	case c := <-accepted:
		_ = c.Close()
		t.Fatalf("a header from %s was honored", c.RemoteAddr())
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProxyListener_HonorsAHeaderFromATrustedNetwork(t *testing.T) {
	t.Parallel()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listener, err := proxyListener(inner, &config.Config{
		ProxyProtocol:     true,
		ProxyTrustedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	sendProxyHeader(t, inner.Addr().String())
	conn, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	assert.Equal(t, "203.0.113.7:40000", conn.RemoteAddr().String())
}
