package ratelimit_test

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/terminal-card/internal/ratelimit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlidingWindow_Allow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limit    int
		window   time.Duration
		requests int
		wantLast bool
		wantSize int
	}{
		{
			name:     "under limit",
			limit:    3,
			window:   time.Second,
			requests: 2,
			wantLast: true,
			wantSize: 1,
		},
		{
			name:     "at limit then deny",
			limit:    2,
			window:   time.Second,
			requests: 3,
			wantLast: false,
			wantSize: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			limiter := ratelimit.New(tt.limit, tt.window)

			var last bool
			for range tt.requests {
				last = limiter.Allow("1.2.3.4")
			}
			assert.Equal(t, tt.wantLast, last)
			assert.Equal(t, tt.wantSize, limiter.Size())
		})
	}
}

func TestSlidingWindow_IndependentIPs(t *testing.T) {
	t.Parallel()
	limiter := ratelimit.New(1, time.Second)

	require.True(t, limiter.Allow("10.0.0.1"))
	require.False(t, limiter.Allow("10.0.0.1"))
	require.True(t, limiter.Allow("10.0.0.2"))
	assert.Equal(t, 2, limiter.Size())
}

func TestSlidingWindow_WindowExpiryEvicts(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const window = 20 * time.Millisecond
		limiter := ratelimit.New(1, window)

		require.True(t, limiter.Allow("1.1.1.1"))
		require.False(t, limiter.Allow("1.1.1.1"))
		assert.Equal(t, 1, limiter.Size())

		time.Sleep(window)
		require.True(t, limiter.Allow("1.1.1.1"), "a call exactly one window old no longer counts")
		assert.Equal(t, 1, limiter.Size())
	})
}

func TestSlidingWindow_MaxKeys(t *testing.T) {
	t.Parallel()
	limiter := ratelimit.New(1, time.Minute).WithMaxKeys(2)

	require.True(t, limiter.Allow("a"))
	require.True(t, limiter.Allow("b"))
	require.True(t, limiter.Allow("c"), "a caller the table has no room for is still served")
	assert.LessOrEqual(t, limiter.Size(), 2, "and the table stays bounded")
}

func TestSlidingWindow_EvictionKeepsTheTableBounded(t *testing.T) {
	t.Parallel()
	limiter := ratelimit.New(1, time.Minute).WithMaxKeys(64)

	for i := range 1_000 {
		require.True(t, limiter.Allow(strconv.Itoa(i)), "every first request is within budget")
		require.LessOrEqual(t, limiter.Size(), 64)
	}
}

func BenchmarkAllow(b *testing.B) {
	for _, keys := range []int{1, 64, 10_000} {
		b.Run(fmt.Sprintf("keys=%d", keys), func(b *testing.B) {
			l := ratelimit.New(1_000_000, time.Minute)
			ids := make([]string, keys)
			for i := range keys {
				ids[i] = strconv.Itoa(i)
				l.Allow(ids[i])
			}

			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				l.Allow(ids[i%keys])
			}
		})
	}
}

func TestSlidingWindow_NonPositiveMaxKeysIsIgnored(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1} {
		limiter := ratelimit.New(1, time.Minute).WithMaxKeys(n)

		assert.Truef(t, limiter.Allow("1.1.1.1"), "WithMaxKeys(%d) must keep the default cap", n)
	}
}

func TestSlidingWindow_SweepsExpiredKeysPeriodically(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const window = 20 * time.Millisecond
		limiter := ratelimit.New(1, window)

		for i := range 63 {
			require.True(t, limiter.Allow(strconv.Itoa(i)))
		}
		require.Equal(t, 63, limiter.Size(), "nothing is swept before the sweep is due")

		time.Sleep(4 * window)
		require.True(t, limiter.Allow("late"), "the 64th call is the one that sweeps")

		assert.Equal(t, 1, limiter.Size(), "every caller that has gone quiet is dropped")
	})
}

// The limiter is shared by every ssh connection and every api request, so Allow and
// Size are called from unrelated goroutines all the time.
func TestSlidingWindow_ConcurrentAllowAndSize(t *testing.T) {
	t.Parallel()
	limiter := ratelimit.New(50, time.Second).WithMaxKeys(8)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 300 {
				// Overlapping key ranges, so workers contend on the same map entries.
				limiter.Allow(fmt.Sprintf("key-%d", (worker+i)%12))
			}
		})
	}
	for range 2 {
		wg.Go(func() {
			for range 300 {
				if n := limiter.Size(); n > 8 {
					t.Errorf("table grew past its cap: %d keys", n)
					return
				}
			}
		})
	}
	wg.Wait()
}

// Fail open, and deliberately: refusing new keys once the table is full would let
// whoever filled it lock everybody else out.
func TestSlidingWindow_FullTableStillAdmitsNewKeys(t *testing.T) {
	t.Parallel()
	limiter := ratelimit.New(1, time.Hour).WithMaxKeys(4)
	for i := range 4 {
		require.True(t, limiter.Allow(fmt.Sprintf("filler-%d", i)))
	}
	require.Equal(t, 4, limiter.Size())

	assert.True(t, limiter.Allow("newcomer"), "a new network is admitted, not locked out")
	assert.LessOrEqual(t, limiter.Size(), 4, "and the table is still bounded")
}

// The shape that matters for abuse: a full table taking a fresh key on every call,
// which is what a single IPv6 /48 (65536 networks) buys an attacker. Eviction runs
// under the lock on every one of these, so it must not be a walk of the table.
func BenchmarkAllow_NewKeyAtCapacity(b *testing.B) {
	for _, keys := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("maxKeys=%d", keys), func(b *testing.B) {
			l := ratelimit.New(120, time.Minute).WithMaxKeys(keys)
			for i := range keys {
				l.Allow(strconv.Itoa(i))
			}

			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				l.Allow("fresh-" + strconv.Itoa(i))
			}
		})
	}
}

// Admitting a new key into a full table must cost the same whether the table holds
// ten keys or ten thousand, because that path is exactly what an attacker with a /48
// to spend drives, and it runs under the limiter's only mutex.
//
// Counted rather than timed: the walking implementation this replaced looked at every
// key on every eviction, and a wall-clock ceiling on that flaked under a loaded -race
// run. One key looked at per eviction is the whole contract.
func TestSlidingWindow_NewKeyAtCapacityDoesNotWalkTheTable(t *testing.T) {
	t.Parallel()
	const maxKeys = 10_000
	const fresh = 20_000

	limiter := ratelimit.New(120, time.Minute).WithMaxKeys(maxKeys)
	for i := range maxKeys {
		require.True(t, limiter.Allow(strconv.Itoa(i)))
	}
	require.Equal(t, maxKeys, limiter.Size())
	require.Zero(t, limiter.EvictVisits(), "filling the table evicts nothing")

	for i := range fresh {
		limiter.Allow("flood-" + strconv.Itoa(i))
	}

	assert.Equal(t, uint64(fresh), limiter.EvictVisits(),
		"%d new keys against a full table: eviction must look at one key each, not scan the table", fresh)
	assert.LessOrEqual(t, limiter.Size(), maxKeys, "and the table is still bounded")
}
