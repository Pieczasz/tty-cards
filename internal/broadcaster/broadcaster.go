// Package broadcaster is an in-process, latest-wins fan-out: every subscriber gets its
// own buffered channel, and a full one drops its oldest message rather than blocking
// the sender. Lobbies and game engines publish their live updates through it.
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
	// ErrClosed is Subscribe on a broadcaster Close already ended.
	ErrClosed = errors.New("broadcaster is closed")
	// ErrAtCapacity is Subscribe past the cap New was given.
	ErrAtCapacity = errors.New("broadcaster is at subscriber capacity")
)

type subscriber[T any] struct {
	id      int
	ch      chan T
	dropped int
}

// Broadcaster is built for the single-node deployment.
// Horizontal scaling across servers would need a shared pub/sub (e.g., Watermill
// over Redis); revisit only if the app runs multi-node.
type Broadcaster[T any] struct {
	// Broadcast takes the write lock: it may drain a subscriber's channel on a full
	// buffer, and two concurrent broadcasts draining the same channel would steal
	// messages from each other and from the subscriber. Exclusive sends also make
	// Unsubscribe's close-from-the-receiver-side safe by construction.
	mu             sync.Mutex
	subscribers    map[<-chan T]*subscriber[T]
	nextID         int
	closed         bool
	maxSubscribers int
	dropped        atomic.Int64
}

// New makes a broadcaster that admits at most maxSubscribers at once; zero or less
// means the default of 64. The cap bounds subscribers, not the per-subscriber buffer,
// which is fixed.
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
// Unsubscribe or Close closes it. It fails with ErrClosed or ErrAtCapacity rather than
// hand back a channel nothing will ever send on.
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
			// Buffer full: drop the oldest and enqueue the newest (latest-wins)
			// so slow subscribers don't get stuck on stale state. Logged once per
			// subscriber and counted, not logged per message - a stuck subscriber
			// would otherwise turn every broadcast into a log write under the lock.
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
			// This send cannot block. Broadcast is the only sender and it holds the
			// lock, so nothing refills the slot the receive above freed - and if that
			// receive found nothing, the subscriber had just drained the buffer
			// itself, which leaves even more room.
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

// Dropped is the total number of messages discarded across all subscribers since
// construction. Owners surface it as a metric; a rising count means a subscriber
// is not keeping up.
func (b *Broadcaster[T]) Dropped() int64 {
	return b.dropped.Load()
}
