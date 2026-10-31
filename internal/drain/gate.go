// Package drain is shutdown's "stop admitting, then wait for what got in". The ssh
// server counts session handlers through it and the lobby counts game watchers.
package drain

import (
	"context"
	"sync"
)

// Gate counts work in until it is closed, then lets shutdown wait for that work.
// The zero value is open. A Gate must not be copied after first use.
//
// mu makes Enter and closing atomic with respect to each other: a WaitGroup alone
// permits Add after Wait has seen zero, so work admitted at the moment the drain
// starts would run untracked.
type Gate struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	// done is closed by the one waiter goroutine Wait ever starts. It is created
	// lazily and reused, so a timed-out drain that is retried does not strand a
	// waiter per attempt.
	done chan struct{}
}

// Enter counts one unit of work in, unless the gate is closed. release gives the
// count back; it is safe to call twice, and a no-op when ok is false, so a caller
// with two exit paths cannot drive the count negative.
func (g *Gate) Enter() (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return func() {}, false
	}
	g.wg.Add(1)
	return sync.OnceFunc(g.wg.Done), true
}

// Close stops Enter admitting work, without waiting for what is already in.
func (g *Gate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// Closed reports whether the gate has stopped admitting work, by Close or Wait.
func (g *Gate) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// Wait closes the gate, then blocks until every admitted unit is released or ctx
// ends, and reports whether everything drained. Because the gate is closed, the
// count only goes down, so the shared waiter exits once the last unit does.
func (g *Gate) Wait(ctx context.Context) bool {
	g.mu.Lock()
	g.closed = true
	if g.done == nil {
		ch := make(chan struct{})
		g.done = ch
		go func() {
			g.wg.Wait()
			close(ch)
		}()
	}
	done := g.done
	g.mu.Unlock()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
