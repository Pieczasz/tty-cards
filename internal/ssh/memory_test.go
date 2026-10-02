package ssh

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// usersByKey gives every fingerprint an account of its own, so concurrent sessions
// do not displace one another the way stubUserRepo's single user would.
type usersByKey struct {
	db.Authenticator
	mu    sync.Mutex
	users map[string]*db.User
}

func (r *usersByKey) LoadUserByFingerprint(_ context.Context, fp string) (*db.User, *db.PublicSSHKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[fp]
	if !ok {
		u = &db.User{ID: testutil.UID(len(r.users) + 1), Username: "bench"}
		r.users[fp] = u
	}
	return u, &db.PublicSSHKey{}, nil
}

func (*usersByKey) UpdateUserActivity(context.Context, *db.User, *db.PublicSSHKey) error { return nil }

// countingDiscard is a session's stdout: the first byte is the first frame.
type countingDiscard struct{ n atomic.Int64 }

func (w *countingDiscard) Write(p []byte) (int, error) { w.n.Add(int64(len(p))); return len(p), nil }

// sessionHeap opens n sessions at once, each asking for a terminal of size, and
// returns the heap one of them holds - ssh connection, bubbletea program and the
// home screen - once all have drawn their first frame.
func sessionHeap(tb testing.TB, addr string, tracker *SessionTracker, n int, size termSize) float64 {
	tb.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	clients := make([]*gossh.Client, n)
	outs := make([]*countingDiscard, n)
	for i := range clients {
		clients[i] = dialInProcess(tb, addr, "bench")
		s, err := clients[i].NewSession()
		require.NoError(tb, err)
		outs[i] = &countingDiscard{}
		s.Stdout = outs[i]
		s.Stderr = io.Discard
		require.NoError(tb, s.RequestPty("xterm-256color", size.h, size.w, gossh.TerminalModes{}))
		require.NoError(tb, s.Shell())
	}
	require.Eventually(tb, func() bool {
		if tracker.Count() != n {
			return false
		}
		for _, o := range outs {
			if o.n.Load() == 0 {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "the sessions never drew a frame")
	time.Sleep(500 * time.Millisecond) // let every first frame settle

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	for _, c := range clients {
		_ = c.Close()
	}
	require.Eventually(tb, func() bool { return tracker.Count() == 0 }, 10*time.Second, 10*time.Millisecond)
	return float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / float64(n) / (1 << 20)
}

// The renderer sizes its cell buffers from the size bubbletea is first given, which
// never passes through the message filter: clamping only resizes let a client that
// asked for 2000x600 at pty-req hold over 100 MiB for the life of its session. Now
// five such sessions share room above the floor for two at the top tier, and what
// they hold together stays within it: five floors plus that room, at ~260 B a cell,
// and ~0.5 MiB a session for the connection and the home screen.
//
//nolint:paralleltest // measures the process heap, so nothing may run beside it
func TestSession_HeapIsBoundedByTheCellBudget(t *testing.T) {
	const n = 5
	top, floor := sizeTiers[len(sizeTiers)-1], sizeTiers[0]
	extra := 2 * (top.cells() - floor.cells())
	reg := newSessionRegistry(cellBudget, extra)
	addr, tracker := startServerWithRegistry(t, &usersByKey{users: map[string]*db.User{}}, reg)

	perSession := sessionHeap(t, addr, tracker, n, termSize{2000, 600})
	bound := (float64(n*floor.cells()+extra)*260/(1<<20) + n*0.5) / n
	t.Logf("%.2f MiB/session, bound %.2f MiB", perSession, bound)
	require.Less(t, perSession, bound, "the sessions hold more than the cell budget granted them")
}

// BenchmarkSessionMemory reports the heap one session holds at each size tier.
// Sessions x MiB/session at the floor, plus extraCellBudget's worth, has to fit
// GOMEMLIMIT; see cellBudget.
//
//	go test -run '^$' -bench SessionMemory -benchtime 3x ./internal/ssh/
func BenchmarkSessionMemory(b *testing.B) {
	for _, size := range sizeTiers {
		b.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(b *testing.B) {
			addr, tracker := startServerWith(b, &usersByKey{users: map[string]*db.User{}})
			for b.Loop() {
				b.ReportMetric(sessionHeap(b, addr, tracker, 20, size), "MiB/session")
			}
		})
	}
}
