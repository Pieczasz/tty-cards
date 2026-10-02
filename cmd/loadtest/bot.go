package main

import (
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// pace is how fast a bot acts: think is the spread a player takes over a move, menu
// the pause between keys everywhere else.
type pace struct {
	thinkMin, thinkMax time.Duration
	menu               time.Duration
}

//nolint:gosec // G404: think-time jitter, not a secret
func jitter(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo)
}

func (p pace) think() time.Duration { return jitter(p.thinkMin, p.thinkMax) }
func (p pace) key() time.Duration   { return jitter(p.menu/2, p.menu*3/2) }

// table is one lobby's rendezvous: the host publishes the code it reads off its
// lobby screen and the guests type it.
type table struct {
	once  sync.Once
	ready chan struct{}
	code  string
}

func newTable() *table { return &table{ready: make(chan struct{})} }

func (t *table) publish(code string) {
	t.once.Do(func() {
		t.code = code
		close(t.ready)
	})
}

// botStats is what a bot reports; results implements it for a run.
type botStats interface {
	moved(rtt time.Duration)
	notApplied()
	gameOver()
	fail(err error)
}

// Timeouts for a screen that does not react. They are well past a loaded server's
// round trip; tripping one means the bot misread the screen or the server is stuck.
const (
	settleTimeout = 5 * time.Second
	joinTimeout   = 15 * time.Second
	lobbyPatience = 60 * time.Second
)

var errParked = errors.New("bot parked")

// bot plays one seat of a Crazy Eights table through the real TUI: it reads the
// screen, decides, waits like a player would and types.
type bot struct {
	host  bool
	seats int
	table *table
	scr   *screen
	keys  io.Writer
	pace  pace
	stats botStats
	stop  <-chan struct{}

	seated     bool      // has reached a lobby; home after that means the seat is gone
	readied    bool      // pressed ready since the last game started
	lobbySince time.Time // when this lobby visit began, for the host's patience
	typedAt    time.Time // when the guest typed the code
	overAt     time.Time // when esc left the last game-over screen
	pending    *pendingMove
	forceDraw  bool // the last move was not applied; draw, which is always legal

	// sentVer is the screen version when keys were last sent. A frame no newer is
	// one the server drew before it read them, so acting on it would repeat them.
	sentVer uint64
	sentAt  time.Time
}

// pendingMove is a move sent and not yet seen applied.
type pendingMove struct {
	at     time.Time
	hand   int
	picker bool // an eight is waiting on the suit picker, the play is not sent yet
}

// run drives the bot until stop closes or it parks.
func (b *bot) run() {
	for {
		lines, ver, changed := b.scr.snapshot()
		wait, err := time.Second, error(nil)
		if ver > b.sentVer || time.Since(b.sentAt) > settleTimeout {
			wait, err = b.step(lines)
		}
		if err != nil {
			if !errors.Is(err, errParked) {
				b.stats.fail(err)
			}
			<-b.stop
			return
		}
		select {
		case <-b.stop:
			return
		case <-changed:
		case <-time.After(wait):
		}
	}
}

// step acts on one frame and says how long to wait for the next if nothing changes.
func (b *bot) step(lines []string) (time.Duration, error) {
	switch classify(lines) {
	case viewHome:
		return b.home()
	case viewCreate:
		return time.Second, b.press(createKey(lines, b.seats))
	case viewJoin:
		return b.join(lines)
	case viewLobby:
		return b.lobby(lines)
	case viewLobbyLeave, viewForfeit:
		return time.Second, b.press("n")
	case viewGame:
		return b.game(lines)
	case viewGameOver:
		return b.gameOverScreen()
	case viewUnknown:
	}
	return time.Second, nil
}

// press waits a menu beat and types keys; an empty key is nothing to do.
func (b *bot) press(keys string) error {
	if keys == "" {
		return nil
	}
	if !b.sleep(b.pace.key()) {
		return errParked
	}
	return b.send(keys)
}

func (b *bot) send(keys string) error {
	_, b.sentVer, _ = b.scr.snapshot()
	b.sentAt = time.Now()
	if _, err := io.WriteString(b.keys, keys); err != nil {
		return errParked // the session is gone; the client reports why
	}
	return nil
}

func (b *bot) sleep(d time.Duration) bool {
	select {
	case <-b.stop:
		return false
	case <-time.After(d):
		return true
	}
}

func (b *bot) home() (time.Duration, error) {
	if b.seated {
		return 0, errors.New("play: back on the home screen, the seat is gone")
	}
	if b.host {
		return time.Second, b.press("n")
	}
	select {
	case <-b.stop:
		return 0, errParked
	case <-b.table.ready:
	}
	return time.Second, b.press("f")
}

func (b *bot) join(lines []string) (time.Duration, error) {
	text := strings.Join(lines, "\n")
	switch {
	case !b.typedAt.IsZero() && time.Since(b.typedAt) > joinTimeout:
		return 0, errors.New("play: joining by code was refused: " + errorLine(lines))
	case !b.typedAt.IsZero():
		return time.Second, nil
	case !strings.Contains(text, "Entering private lobby code"):
		return time.Second, b.press("c")
	}
	for _, r := range b.table.code {
		if err := b.press(string(r)); err != nil {
			return 0, err
		}
	}
	b.typedAt = time.Now()
	return time.Second, b.press(keyEnter)
}

// errorLine is the "Error: ..." line a screen shows, for a failure report.
func errorLine(lines []string) string {
	for _, line := range lines {
		if _, msg, ok := strings.Cut(line, "Error: "); ok {
			return strings.Trim(msg, " │")
		}
	}
	return "no error on screen"
}

func (b *bot) lobby(lines []string) (time.Duration, error) {
	if !b.seated || b.lobbySince.IsZero() {
		b.seated, b.lobbySince = true, time.Now()
	}
	if b.host {
		code := parseLobbyCode(lines)
		if code == "" {
			return time.Second, nil
		}
		b.table.publish(code)
	}
	if b.readied || !b.mayReady(lines) {
		return time.Second, nil
	}
	b.readied = true
	return time.Second, b.press("r")
}

// mayReady: a guest readies at once. The host readies last, once every seat is
// taken - the table starts when everyone seated is ready - or once it has waited long
// enough that a seat is clearly not coming.
func (b *bot) mayReady(lines []string) bool {
	if !b.host {
		return true
	}
	guests := guestCount(lines)
	return guests >= b.seats-1 || (guests > 0 && time.Since(b.lobbySince) > lobbyPatience)
}

func (b *bot) gameOverScreen() (time.Duration, error) {
	if b.pending != nil {
		// The winning card: the frame showing it applied is this one.
		b.stats.moved(time.Since(b.pending.at))
		b.pending = nil
	}
	if !b.overAt.IsZero() && time.Since(b.overAt) < settleTimeout {
		return time.Second, nil
	}
	if b.overAt.IsZero() && b.host {
		b.stats.gameOver()
	}
	if !b.sleep(b.pace.think()) {
		return 0, errParked
	}
	b.overAt = time.Now()
	return time.Second, b.send(keyEsc)
}

func (b *bot) game(lines []string) (time.Duration, error) {
	// A game is on: the next lobby visit readies again and the next game over counts.
	b.readied, b.overAt, b.lobbySince = false, time.Time{}, time.Time{}
	t := parseCrazy(lines)
	if b.pending != nil && !b.settle(t) {
		return time.Second, nil
	}
	switch {
	case t.picker && t.myTurn:
		// Spades, the picker's first choice: which suit is no load question.
		if !b.sleep(b.pace.key()) {
			return 0, errParked
		}
		b.pending = &pendingMove{at: time.Now(), hand: len(t.hand)}
		return time.Second, b.send(keyEnter)
	case !t.myTurn:
		return time.Second, nil
	}
	if !b.sleep(b.pace.think()) {
		return 0, errParked
	}
	// The frame read before the pause may be stale by now.
	lines, _, _ = b.scr.snapshot()
	if t = parseCrazy(lines); !t.myTurn || t.picker {
		return 0, nil
	}
	m := t.choose()
	if b.forceDraw {
		m = move{keys: "d"}
	}
	b.pending = &pendingMove{at: time.Now(), hand: len(t.hand), picker: m.wild}
	return time.Second, b.send(m.keys)
}

// settle checks the move in flight against the frame and reports whether the bot is
// free to act again. A play or draw always ends the turn and changes the hand, so
// either is the frame showing it applied.
func (b *bot) settle(t crazyTable) bool {
	p := b.pending
	switch {
	case p.picker && t.picker:
		b.pending = nil
		return true
	case !p.picker && (!t.myTurn || len(t.hand) != p.hand):
		b.stats.moved(time.Since(p.at))
		b.pending, b.forceDraw = nil, false
		return true
	case time.Since(p.at) > settleTimeout:
		b.stats.notApplied()
		b.pending, b.forceDraw = nil, true
		return true
	}
	return false
}
