package main

import (
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	charmssh "charm.land/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The percentiles are the tool's output, so an off-by-one here misreports every run.
func TestPct(t *testing.T) {
	t.Parallel()
	ten := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		name   string
		sorted []float64
		p      float64
		want   float64
	}{
		{name: "no samples", sorted: nil, p: 50, want: 0},
		{name: "one sample is every percentile", sorted: []float64{7}, p: 95, want: 7},
		{name: "median rounds down", sorted: ten, p: 50, want: 5},
		{name: "p95 of ten", sorted: ten, p: 95, want: 9},
		{name: "p100 is the max", sorted: ten, p: 100, want: 10},
		{name: "p0 is the min", sorted: ten, p: 0, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, pct(tt.sorted, tt.p), 0)
		})
	}
}

func TestDrain(t *testing.T) {
	t.Parallel()

	t.Run("counts every byte and times the first", func(t *testing.T) {
		t.Parallel()
		done := make(chan drainResult, 1)
		var sink strings.Builder
		drain(iotest.OneByteReader(strings.NewReader("frame")), time.Now().Add(-time.Second), &sink, done)

		res := <-done
		assert.Equal(t, int64(5), res.bytes)
		assert.Equal(t, 5, res.reads, "every read is a burst of output the server pushed")
		assert.Equal(t, "frame", sink.String(), "the screen sees every byte")
		assert.GreaterOrEqual(t, res.firstFrame, time.Second)
		require.ErrorIs(t, res.err, io.EOF)
	})

	t.Run("no output means no first frame", func(t *testing.T) {
		t.Parallel()
		done := make(chan drainResult, 1)
		drain(strings.NewReader(""), time.Now(), io.Discard, done)

		res := <-done
		assert.Zero(t, res.firstFrame, "a session that never drew must not report a latency")
		assert.Zero(t, res.bytes)
	})

	t.Run("a transport error is reported, not swallowed", func(t *testing.T) {
		t.Parallel()
		reset := errors.New("connection reset")
		done := make(chan drainResult, 1)
		drain(iotest.ErrReader(reset), time.Now(), io.Discard, done)

		assert.ErrorIs(t, (<-done).err, reset)
	})
}

// Players are seated in connect order, spread through the ramp, in whole tables with
// the host connecting first - a guest ahead of its host would wait on a code no one
// has published.
func TestPlanRoles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sessions int
		fraction float64
		seats    int
		players  int
	}{
		{name: "nobody plays by default", sessions: 10, fraction: 0, seats: 4, players: 0},
		{name: "a mixed run", sessions: 1000, fraction: 0.4, seats: 4, players: 400},
		{name: "rounded down to whole tables", sessions: 10, fraction: 0.5, seats: 4, players: 4},
		{name: "everyone", sessions: 9, fraction: 1, seats: 3, players: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			roles := planRoles(tt.sessions, tt.fraction, tt.seats, pace{})

			seen := map[*table][]bool{}
			var order []*table
			firstPlayer, lastPlayer := -1, -1
			for i, r := range roles {
				if r.table == nil {
					continue
				}
				if firstPlayer < 0 {
					firstPlayer = i
				}
				lastPlayer = i
				if _, ok := seen[r.table]; !ok {
					order = append(order, r.table)
				}
				seen[r.table] = append(seen[r.table], r.host)
			}
			assert.Len(t, order, tt.players/tt.seats, "tables")
			for _, hosts := range seen {
				require.Len(t, hosts, tt.seats)
				assert.Equal(t, []bool{true}, slices.Compact(hosts[:1]), "the host connects first")
				assert.NotContains(t, hosts[1:], true, "one host per table")
			}
			if tt.players > 0 && tt.players < tt.sessions {
				assert.Greater(t, lastPlayer-firstPlayer, tt.sessions/2, "players are spread through the ramp")
			}
		})
	}
}

// The refusals the owner has to act on are told apart, each naming the setting.
func TestRefusalsAreNamed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "auth refused", err: dialError(errors.New("ssh: handshake failed: ssh: unable to authenticate")), want: "RATE_LIMIT_CONNECTIONS"},
		{name: "dial timeout", err: dialError(&net.OpError{Op: "dial", Err: timeoutError{}}), want: "MAX_CONNECTIONS"},
		{name: "other dial error", err: dialError(errors.New("connection refused")), want: "dial/auth: connection refused"},
		{name: "registration budget", err: endedError("too many new accounts from your network; please try again later\n"), want: "REGISTRATION_LIMIT"},
		{name: "name taken", err: endedError("could not register that name; try another with ssh -l <name>\n"), want: "-prefix"},
		{name: "server full", err: endedError("The server is full right now - please try again in a few minutes.\n"), want: "MAX_CONNECTIONS"},
		{name: "silent hang-up", err: endedError(""), want: "no message"},
		{name: "anything else is quoted", err: endedError("\nboom\nmore"), want: "server: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, tt.err.Error(), tt.want)
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// refusingServer is an ssh server that accepts any key and refuses every session the
// way the real one does - a line on stderr and a non-zero exit, the shape wish.Fatalf
// gives it - with message.
func refusingServer(t *testing.T, message string) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &charmssh.Server{
		PublicKeyHandler: func(charmssh.Context, charmssh.PublicKey) bool { return true },
		Handler: func(s charmssh.Session) {
			_, _ = io.WriteString(s.Stderr(), message)
			_ = s.Exit(1)
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// A server that hangs up at once is a refusal with a reason on stderr, and the report
// has to carry that reason - the budget to raise - not "ended with no message".
func TestClient_NamesTheServersRefusal(t *testing.T) {
	t.Parallel()
	for range 20 {
		addr := refusingServer(t, "too many new accounts from your network; please try again later\n")
		r := &results{addr: addr, journey: "idle", prefix: "t"}

		r.client(0, role{}, make(chan struct{}))

		require.Equal(t, map[string]int{errRegistration: 1}, r.errs)
	}
}

func TestTermSize_Set(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in      string
		want    termSize
		wantErr bool
	}{
		{in: "120x40", want: termSize{120, 40}},
		{in: "80x24", want: termSize{80, 24}},
		{in: "120", wantErr: true},
		{in: "10x5", wantErr: true},
		{in: "wide", wantErr: true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			var got termSize
			err := got.Set(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.in, got.String())
		})
	}
}
