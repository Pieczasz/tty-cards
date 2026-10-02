// Package broadcaster is an in-process, latest-wins fan-out: every subscriber gets its
// own buffered channel, and a full one drops its oldest message. Lobbies and game engines
// publish their live updates through it.
package broadcaster

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

const defaultMaxSubscribers = 64
const subscriberBuffer = 256

var (
	ErrClosed     = errors.New("broadcaster is closed")
	ErrAtCapacity = errors.New("broadcaster is at subscriber capacity")
)

// Broadcaster is built for the single-node deployment.
// Horizontal scaling across servers would need a shared pub/sub (e.g., Watermill
// over Redis); revisit only if the app runs multi-node.
type Broadcaster[T any] struct {
	// Every send happens under mu, which is what makes Unsubscribe's close from the
	// receiving side safe and stops two broadcasts draining the same full channel.
	mu             sync.Mutex
	subscribers    map[<-chan T]*subscriber[T]
	nextID         int
	closed         bool
	maxSubscribers int
	dropped        atomic.Int64
}

type subscriber[T any] struct {
	id      int
	ch      chan T
	dropped int
}

// New makes a broadcaster that admits at most maxSubscribers at once. The cap bounds
// subscribers, not the per-subscriber buffer, which is fixed.
func New[T any](maxSubscribers int) *Broadcaster[T] {
	if maxSubscribers <= 0 {
		maxSubscribers = defaultMaxSubscribers
	}
	return &Broadcaster[T]{
		subscribers:    make(map[<-chan T]*subscriber[T], maxSubscribers),
		maxSubscribers: maxSubscribers,
	}
}

// Subscribe returns a fresh channel that receives every later Broadcast until
// Unsubscribe or Close closes it.
func (b *Broadcaster[T]) Subscribe() (<-chan T, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrClosed
	}
	if len(b.subscribers) >= b.maxSubscribers {
		return nil, fmt.Errorf("%w of %d", ErrAtCapacity, b.maxSubscribers)
	}

	ch := make(chan T, subscriberBuffer)
	b.subscribers[ch] = &subscriber[T]{ch: ch, id: b.nextID}
	b.nextID++
	return ch, nil
}

// Unsubscribe closes ch and frees its slot. An unknown or already-closed channel is a
// no-op, so it is safe after Close.
func (b *Broadcaster[T]) Unsubscribe(ch <-chan T) {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub, ok := b.subscribers[ch]
	if !ok {
		return
	}
	delete(b.subscribers, ch)
	close(sub.ch)
}

// Broadcast delivers msg to every subscriber without blocking: a full channel loses its
// oldest message instead. A no-op after Close.
func (b *Broadcaster[T]) Broadcast(msg T) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}

	for _, sub := range b.subscribers {
		select {
		case sub.ch <- msg:
		default:
			b.dropped.Add(1)
			sub.dropped++
			if sub.dropped == 1 {
				slog.Warn("broadcaster channel is full, dropping oldest messages",
					"subscriber_id", sub.id)
			}
			select {
			case <-sub.ch:
			default:
			}
			sub.ch <- msg
		}
	}
}

// Close closes every subscriber's channel and refuses new ones. Safe to call repeatedly.
func (b *Broadcaster[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for _, sub := range b.subscribers {
		close(sub.ch)
	}
	clear(b.subscribers)
}

// Len is the number of live subscribers.
func (b *Broadcaster[T]) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

// Dropped is how many messages full subscribers have lost, across all of them.
func (b *Broadcaster[T]) Dropped() int64 {
	return b.dropped.Load()
}
