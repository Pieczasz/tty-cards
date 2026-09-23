package broadcaster

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBroadcaster_Subscribe(t *testing.T) {
	t.Parallel()
	b := New[string](10)

	ch := mustSubscribe(t, b)
	assert.NotNil(t, ch)

	ch2 := mustSubscribe(t, b)
	assert.NotNil(t, ch2)

	b.mu.Lock()
	assert.Len(t, b.subscribers, 2)
	b.mu.Unlock()
}

func TestBroadcaster_Broadcast(t *testing.T) {
	t.Parallel()
	b := New[int](10)

	ch1 := mustSubscribe(t, b)
	ch2 := mustSubscribe(t, b)

	b.Broadcast(42)

	got1, _ := recv(t, ch1)
	assert.Equal(t, 42, got1)
	got2, _ := recv(t, ch2)
	assert.Equal(t, 42, got2)
}

func TestBroadcaster_Unsubscribe(t *testing.T) {
	t.Parallel()
	b := New[string](10)

	ch1 := mustSubscribe(t, b)
	ch2 := mustSubscribe(t, b)

	b.mu.Lock()
	assert.Len(t, b.subscribers, 2)
	b.mu.Unlock()

	b.Unsubscribe(ch1)

	b.mu.Lock()
	assert.Len(t, b.subscribers, 1)
	b.mu.Unlock()

	_, ok := recv(t, ch1)
	assert.False(t, ok, "the unsubscribed channel must be closed")

	b.Broadcast("hello")

	got2, _ := recv(t, ch2)
	assert.Equal(t, "hello", got2)
}

func TestBroadcaster_SubscribeAfterClose(t *testing.T) {
	t.Parallel()
	b := New[int](10)
	b.Close()

	ch, err := b.Subscribe()
	require.ErrorIs(t, err, ErrClosed, "a closed broadcaster must say so, not hand back a dead channel")
	assert.Nil(t, ch)
	b.Close()
}

func TestBroadcaster_Close(t *testing.T) {
	t.Parallel()
	b := New[int](10)

	ch1 := mustSubscribe(t, b)
	ch2 := mustSubscribe(t, b)

	b.Close()

	b.mu.Lock()
	assert.Empty(t, b.subscribers)
	b.mu.Unlock()

	_, ok1 := <-ch1
	assert.False(t, ok1)

	_, ok2 := <-ch2
	assert.False(t, ok2)
}

func TestBroadcaster_NonBlockingFullChannel(t *testing.T) {
	t.Parallel()
	b := New[int](1)

	ch := mustSubscribe(t, b)

	for i := range 256 {
		b.Broadcast(i)
	}

	done := make(chan bool)
	go func() {
		b.Broadcast(1001)
		done <- true
	}()
	recv(t, done)

	var last int
	for {
		select {
		case v := <-ch:
			last = v
			continue
		default:
		}
		break
	}
	assert.Equal(t, 1001, last, "most recent broadcast should survive a full buffer")
}

func TestBroadcaster_LatestWins(t *testing.T) {
	t.Parallel()
	b := New[int](10)

	ch := mustSubscribe(t, b)

	const total = 512
	for i := range total {
		b.Broadcast(i)
	}

	var values []int
	for {
		select {
		case v := <-ch:
			values = append(values, v)
			continue
		default:
		}
		break
	}

	if assert.NotEmpty(t, values) {
		assert.Equal(t, total-1, values[len(values)-1], "latest message must survive")
		assert.NotContains(t, values, 0, "oldest message should have been dropped")
	}
}

// Every one of these operations has to run against a live broadcaster: closing it a
// millisecond in turns the rest into no-ops and the race detector sees nothing.
func TestBroadcaster_ConcurrentStress(t *testing.T) {
	t.Parallel()
	b := New[int](32)

	const (
		broadcasters = 8
		subscribers  = 8
		iterations   = 500
	)

	var wg sync.WaitGroup

	for range broadcasters {
		wg.Go(func() {
			for i := range iterations {
				b.Broadcast(i)
			}
		})
	}

	for range subscribers {
		wg.Go(func() {
			for range iterations {
				ch, err := b.Subscribe()
				if err != nil {
					continue
				}
				select {
				case <-ch:
				default:
				}
				b.Unsubscribe(ch)
			}
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent stress test deadlocked")
	}

	b.Close()
	assert.Equal(t, 0, b.Len(), "all subscribers should be gone after Close")
}

// Close racing live traffic is its own case: Subscribe must report the closure rather
// than hand back a channel nothing will ever send on, Broadcast must not send on a
// channel Close is closing, and Unsubscribe must not double-close one.
func TestBroadcaster_CloseRacesSubscribeAndBroadcast(t *testing.T) {
	t.Parallel()
	b := New[int](32)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for range 8 {
		wg.Go(func() {
			<-start
			for i := range 500 {
				b.Broadcast(i)
			}
		})
		wg.Go(func() {
			<-start
			for range 500 {
				ch, err := b.Subscribe()
				if err != nil {
					require.ErrorIs(t, err, ErrClosed, "the only reason to refuse here")
					continue
				}
				b.Unsubscribe(ch)
			}
		})
	}
	wg.Go(func() {
		<-start
		b.Close()
	})

	close(start)
	wg.Wait()

	assert.Zero(t, b.Len())
	_, err := b.Subscribe()
	assert.ErrorIs(t, err, ErrClosed)
}

// Dropped is the metric the lobby logs when a view stops keeping up, so it has to
// count exactly the messages that were discarded and not move otherwise.
func TestBroadcaster_DroppedCountsDiscardedMessages(t *testing.T) {
	t.Parallel()
	b := New[int](4)
	t.Cleanup(b.Close)

	ch := mustSubscribe(t, b)

	for i := range subscriberBuffer {
		b.Broadcast(i)
	}
	require.Zero(t, b.Dropped(), "a buffer that exactly fills has dropped nothing")

	const overflow = 7
	for range overflow {
		b.Broadcast(0)
	}
	assert.EqualValues(t, overflow, b.Dropped(), "one drop per message past the buffer")
	assert.Len(t, ch, subscriberBuffer, "and the buffer stays full rather than growing")
}

// A subscriber that has gone away entirely must not stall the table: the warning is
// logged once and the feed keeps moving for everybody else.
func TestBroadcaster_ASlowSubscriberDoesNotBlockTheOthers(t *testing.T) {
	t.Parallel()
	b := New[int](4)
	t.Cleanup(b.Close)

	stalled := mustSubscribe(t, b)
	keeping := mustSubscribe(t, b)

	for i := range subscriberBuffer + 10 {
		b.Broadcast(i)
		if i < subscriberBuffer {
			continue
		}
		// The attentive subscriber drains as it goes, so only the stalled one is
		// ever full.
		<-keeping
	}

	assert.Positive(t, b.Dropped())
	assert.Len(t, stalled, subscriberBuffer)
	assert.NotEmpty(t, keeping, "the attentive subscriber is still being served")
}

func TestBroadcaster_MaxSubscribers(t *testing.T) {
	t.Parallel()
	b := New[int](2)
	ch1 := mustSubscribe(t, b)
	ch2 := mustSubscribe(t, b)
	ch3, err := b.Subscribe()

	assert.Equal(t, 2, b.Len())
	require.ErrorIs(t, err, ErrAtCapacity, "over-capacity subscribe must report why")
	assert.Nil(t, ch3)

	b.Unsubscribe(ch1)
	ch4 := mustSubscribe(t, b)
	assert.Equal(t, 2, b.Len())
	select {
	case <-ch4:
		t.Fatal("new subscriber channel should stay open")
	default:
	}
	_ = ch2
}

func TestBroadcaster_NonPositiveCapacityGetsTheDefault(t *testing.T) {
	t.Parallel()

	for _, capacity := range []int{0, -1} {
		b := New[int](capacity)
		t.Cleanup(b.Close)

		_, err := b.Subscribe()
		require.NoErrorf(t, err, "New(%d) must fall back to a usable capacity", capacity)
		assert.Equal(t, defaultMaxSubscribers, b.maxSubscribers)
	}
}

func BenchmarkBroadcast(b *testing.B) {
	for _, subs := range []int{1, 4, 9} {
		b.Run(fmt.Sprintf("subscribers=%d", subs), func(b *testing.B) {
			bc := New[int](subs + 8)
			defer bc.Close()

			done := make(chan struct{})
			var wg sync.WaitGroup
			for range subs {
				ch, err := bc.Subscribe()
				if err != nil {
					b.Fatal(err)
				}
				wg.Go(func() {
					for {
						select {
						case <-ch:
						case <-done:
							return
						}
					}
				})
			}

			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				bc.Broadcast(i)
			}
			b.StopTimer()
			close(done)
			wg.Wait()
		})
	}
}

// recv waits generously: a message that is coming arrives in microseconds, and a tight
// deadline only flakes under -race on a loaded machine.
func recv[T any](t *testing.T, ch <-chan T) (T, bool) {
	t.Helper()
	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(10 * time.Second):
		t.Fatal("nothing arrived on the channel")
	}
	var zero T
	return zero, false
}

func mustSubscribe[T any](t *testing.T, b *Broadcaster[T]) <-chan T {
	t.Helper()
	ch, err := b.Subscribe()
	require.NoError(t, err)
	return ch
}
