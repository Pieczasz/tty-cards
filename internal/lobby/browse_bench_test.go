package lobby

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/require"
)

// benchGames are the slugs the browse tables play: enough distinct games that
// GamesOnOffer and the per-game rating lookups have work to do.
var benchGames = []string{"crazy_eights", "uno", "poker", "hearts", "gin_rummy"}

func benchRegistry() *game.Registry {
	mods := make([]game.Module, len(benchGames))
	for i, slug := range benchGames {
		mods[i] = game.Module{
			Name:       strings.ToUpper(slug),
			Slug:       slug,
			MinPlayers: 2,
			MaxPlayers: 6,
			Factory:    func() game.Rules { return stubRules{} },
		}
	}
	return game.NewRegistry(mods...)
}

// benchPlayer is a rated player, so a table's average and a browser's distance to it
// are real lookups rather than the unrated fast path.
func benchPlayer(i int) *game.Player {
	ratings := make(map[string]uint32, len(benchGames))
	for g, slug := range benchGames {
		ratings[slug] = uint32(900 + (i*37+g*101)%900)
	}
	return &game.Player{ID: fmt.Sprintf("bench-%06d", i), UserID: testutil.UID(i + 1), Ratings: ratings}
}

// browseFixture is a server with n tables, each a leader and one guest: half public
// and waiting, a quarter private, a quarter mid-game. Only the first half is ever on
// offer, so a browse has to tell them apart.
type browseFixture struct {
	m *Manager
	// public is the tables on offer, which churn flips private and back.
	public []*Lobby
	// browser is a rated player seated nowhere, the one refreshing the join screen.
	browser *game.Player
}

func newBrowseFixture(b *testing.B, n int) browseFixture {
	b.Helper()
	registry := benchRegistry()
	m := newManagerFor(b, nil, registry)
	f := browseFixture{m: m, browser: benchPlayer(2 * n)}
	for i := range n {
		leader, guest := benchPlayer(2*i), benchPlayer(2*i+1)
		l, err := m.CreateLobby(leader, WithCardGame(benchGames[i%len(benchGames)]), WithPrivate(i%4 == 2))
		require.NoError(b, err)
		require.NoError(b, joinErr(m.JoinLobbyByCode(b.Context(), l.Code(), guest)))
		switch i % 4 {
		case 0, 1:
			f.public = append(f.public, l)
		case 3:
			require.NoError(b, m.ToggleReady(leader))
			require.NoError(b, m.ToggleReady(guest))
		}
	}
	require.NotZero(b, f.refresh(), "a browse that finds nothing measures nothing")
	return f
}

// refresh is what one join-screen tick asks of the server (join.go's refresh): the
// list a browser sees, and the games its filter cycles through.
func (f browseFixture) refresh() int {
	entries := f.m.BrowseLobbies(f.browser, BrowseFilter{OnlyWithRoom: true, Limit: MaxBrowseLimit})
	return len(entries) + len(f.m.GamesOnOffer())
}

// churnRates are how often a public table changes its settings while browsers read:
// never, and a thousand times a second - far above what players do, and every one of
// them a write that changes which tables are on offer.
var churnRates = []time.Duration{0, time.Millisecond}

func churnName(every time.Duration) string {
	if every == 0 {
		return "none"
	}
	return every.String()
}

// startChurn flips a public table private and back every interval until the returned
// stop, which waits for the goroutine: goleak checks the package after benchmarks too.
func startChurn(tables []*Lobby, every time.Duration) (stop func()) {
	if every == 0 || len(tables) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			l := tables[(i/2)%len(tables)]
			_ = l.SetPrivate(l.Snapshot().Leader.Player, i%2 == 0)
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

// BenchmarkBrowse is the join screen's cost to the server: every op is one refresh,
// from as many browsers as there are CPUs.
func BenchmarkBrowse(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		for _, churn := range churnRates {
			b.Run(fmt.Sprintf("lobbies=%d/churn=%s", n, churnName(churn)), func(b *testing.B) {
				f := newBrowseFixture(b, n)
				stop := startChurn(f.public, churn)
				defer stop()

				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						f.refresh()
					}
				})
				b.StopTimer()
			})
		}
	}
}

// BenchmarkJoinUnderBrowse is a join and a leave, the writes that take m.mu
// exclusively, while every other CPU refreshes the join screen flat out: what the
// browse costs everyone else on the manager lock.
func BenchmarkJoinUnderBrowse(b *testing.B) {
	const n = 1000
	for _, churn := range churnRates {
		b.Run(fmt.Sprintf("lobbies=%d/churn=%s", n, churnName(churn)), func(b *testing.B) {
			f := newBrowseFixture(b, n)
			// A window so short no join is ever refused, and the per-player log never
			// grows past one entry: Allow's cost stays what a real join pays.
			f.m.joinLimiter = ratelimit.New(1_000_000, time.Microsecond)
			table, err := f.m.CreateLobby(benchPlayer(2*n+1), WithCardGame(benchGames[0]), WithPrivate(false))
			require.NoError(b, err)
			joiner := benchPlayer(2*n + 2)

			stopChurn := startChurn(f.public, churn)
			defer stopChurn()
			stopBrowsers := startBrowsers(f, max(runtime.GOMAXPROCS(0)-1, 1))
			defer stopBrowsers()

			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.m.JoinLobbyByCode(b.Context(), table.Code(), joiner); err != nil {
					b.Fatal(err)
				}
				f.m.LeaveLobby(joiner)
			}
			b.StopTimer()
		})
	}
}

func startBrowsers(f browseFixture, count int) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				f.refresh()
			}
		})
	}
	return func() {
		close(done)
		wg.Wait()
	}
}
