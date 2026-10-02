package main

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is a run's botStats for a test.
type recorder struct {
	mu        sync.Mutex
	moves     int
	unapplied int
	games     int
	errs      []error
}

func (r *recorder) moved(time.Duration) { r.mu.Lock(); r.moves++; r.mu.Unlock() }
func (r *recorder) notApplied()         { r.mu.Lock(); r.unapplied++; r.mu.Unlock() }
func (r *recorder) gameOver()           { r.mu.Lock(); r.games++; r.mu.Unlock() }
func (r *recorder) fail(err error)      { r.mu.Lock(); r.errs = append(r.errs, err); r.mu.Unlock() }

func (r *recorder) gamesDone() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.games
}

// A whole table of bots, each on the real TUI the ssh server runs, sharing one lobby
// manager: the host creates a lobby, the guests join by the code read off its screen,
// they play Crazy Eights to the end and then play again. This is the journey the load
// test drives, minus only the network.
func TestBots_PlayWholeGamesThroughTheRealTUI(t *testing.T) {
	t.Parallel()
	for _, seats := range []int{2, 4} {
		t.Run(fmt.Sprintf("%d_seats", seats), func(t *testing.T) {
			t.Parallel()
			stats := playTable(t, seats, 2)

			assert.Empty(t, stats.errs)
			assert.Zero(t, stats.unapplied, "every move the bots sent was applied")
			assert.Greater(t, stats.moves, 2*seats, "the bots played rather than waited on the clock")
		})
	}
}

// playTable runs one table of seats bots until it has finished games games.
func playTable(t *testing.T, seats, games int) *recorder {
	t.Helper()
	mgr := lobby.NewManager(t.Context(), nil)
	registry := catalog.NewRegistry()
	stats := &recorder{}
	tbl := newTable()
	stop := make(chan struct{})
	var bots sync.WaitGroup

	for seat := range seats {
		model := tui.New(tui.Deps{
			SessionCtx: t.Context(), LobbyManager: mgr, GameRegistry: registry,
			User: db.User{ID: testutil.UID(seat + 1), Username: fmt.Sprintf("bot_%d", seat)},
		})
		_, scr, keys := startProgram(t, model)
		b := &bot{
			host: seat == 0, seats: seats, table: tbl, scr: scr, keys: keys, stats: stats, stop: stop,
			pace: pace{thinkMin: time.Millisecond, thinkMax: 10 * time.Millisecond, menu: 10 * time.Millisecond},
		}
		bots.Go(b.run)
	}
	t.Cleanup(func() {
		close(stop)
		bots.Wait()
	})

	deadline := time.Now().Add(60 * time.Second)
	for stats.gamesDone() < games {
		require.True(t, time.Now().Before(deadline), "only %d of %d games finished", stats.gamesDone(), games)
		time.Sleep(20 * time.Millisecond)
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	return &recorder{moves: stats.moves, unapplied: stats.unapplied, games: stats.games, errs: stats.errs}
}

// A seat the bot had and lost - it is back on the home screen - is reported rather
// than silently turning into a menu session.
func TestBot_ReportsALostSeat(t *testing.T) {
	t.Parallel()
	stats := &recorder{}
	b := &bot{seated: true, stats: stats}

	_, err := b.step([]string{"n - New Game | f - Join Game | p - Profile"})

	require.Error(t, err)
	require.NotErrorIs(t, err, errParked)
	assert.Contains(t, err.Error(), "seat is gone")
}

// The move round-trip is keypress to applied. The eight's suit is confirmed after a
// menu beat, and starting the clock before that beat reported the bot's own pause as
// server latency - half a second on an idle server.
func TestBot_TimesThePickerConfirmFromTheKeypress(t *testing.T) {
	t.Parallel()
	b := &bot{
		scr: newScreen(termWidth, termHeight), keys: io.Discard, stats: &recorder{},
		stop: make(chan struct{}), pace: pace{menu: 100 * time.Millisecond},
	}
	frame := []string{"Pick a suit:", "> YOUR TURN <", "d: draw"}

	before := time.Now()
	_, err := b.step(frame)

	require.NoError(t, err)
	require.NotNil(t, b.pending, "the confirm is a move in flight")
	assert.GreaterOrEqual(t, b.pending.at.Sub(before), 50*time.Millisecond, "the clock started before the pause")
}

// The winning card's frame is the game-over screen. A move still in flight there was
// left pending into the next game and counted the whole lobby round - seconds - as
// its round-trip.
func TestBot_TheGameOverScreenSettlesTheWinningMove(t *testing.T) {
	t.Parallel()
	stats := &recorder{}
	b := &bot{
		scr: newScreen(termWidth, termHeight), keys: io.Discard, stats: stats, stop: make(chan struct{}),
		pending: &pendingMove{at: time.Now(), hand: 1},
	}

	_, err := b.step([]string{"Game Over! Winner: load_0001"})

	require.NoError(t, err)
	assert.Nil(t, b.pending)
	assert.Equal(t, 1, stats.moves)
}
