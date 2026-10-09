package game

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedAction struct{ name string }

func (a namedAction) Name() string { return a.name }

type timeoutRules struct {
	safe    Action
	reject  bool
	mu      sync.Mutex
	applied []string
}

func (r *timeoutRules) OnGameStart(s *State) error    { return dealEach(s, 1) }
func (r *timeoutRules) CheckWinCondition(*State) bool { return false }

func (r *timeoutRules) ValidateAction(_ *State, _ Action) error {
	if r.reject {
		return errors.New("rules reject everything")
	}
	return nil
}

func (r *timeoutRules) ApplyAction(state *State, action Action) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, state.Players[state.CurrentTurn].ID+":"+action.Name())
	return nil
}

func (r *timeoutRules) Standings(state *State) []*Player { return state.Players }

func (r *timeoutRules) TimeoutAction(*State) Action { return r.safe }

func (r *timeoutRules) appliedActions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.applied...)
}

// turnDeadline is for tests; views read the remaining time from Frame.
func (e *Engine) turnDeadline() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock.deadline
}

// missedTurns is for tests: the idle count is the engine's own business.
func (e *Engine) missedTurns(playerID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock.missed[playerID]
}

func fireTurnTimeout(t *testing.T, e *Engine) {
	t.Helper()
	e.mu.Lock()
	seq := e.clock.seq
	e.mu.Unlock()
	e.onTurnTimeout(seq)
}

func newTimeoutEngine(t *testing.T, rules Rules, ids ...string) *Engine {
	t.Helper()
	players := make([]*Player, 0, len(ids))
	for _, id := range ids {
		players = append(players, &Player{ID: id})
	}
	engine := NewEngine(rules, players, WithTurnTimeout(time.Hour))
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())
	return engine
}

func TestEngine_TurnTimeout_PlaysTheSafeMove(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	before := engine.CurrentPlayerID()
	fireTurnTimeout(t, engine)

	assert.Equal(t, []string{before + ":safe"}, rules.appliedActions(),
		"the expired seat's safe move must be played for them")
	assert.Equal(t, 1, engine.missedTurns(before))
	assert.NotEqual(t, before, engine.CurrentPlayerID(), "the turn must move on")
}

func TestEngine_TurnTimeout_OwnActionClearsTheCount(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	first := engine.CurrentPlayerID()
	fireTurnTimeout(t, engine)
	second := engine.CurrentPlayerID()
	fireTurnTimeout(t, engine)
	require.Equal(t, first, engine.CurrentPlayerID(), "two seats, so the turn is back")
	require.Equal(t, 1, engine.missedTurns(first))

	require.NoError(t, engine.SubmitAction(first, namedAction{name: "real"}))

	assert.Zero(t, engine.missedTurns(first), "playing resets the idle count")
	assert.Equal(t, 1, engine.missedTurns(second), "and does not touch anybody else's")
}

func TestEngine_TurnTimeout_TakesTheSeatAfterMaxMissesInARow(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	firstToAct := engine.CurrentPlayerID()
	// Two seats alternating, so the first to act is also the first to reach the limit.
	for range MaxMissedTurns * 2 {
		if engine.IsFinished() {
			break
		}
		fireTurnTimeout(t, engine)
	}

	require.True(t, engine.IsFinished(), "losing a seat leaves one player, which ends the game")
	assert.Zero(t, engine.missedTurns(firstToAct),
		"the count is reaped with the seat, so a future re-entry starts clean")

	var idleFor string
	for {
		select {
		case ev := <-events:
			if ev.Type == EventPlayerIdle {
				idleFor = ev.PlayerID
			}
			continue
		default:
		}
		break
	}
	assert.Equal(t, firstToAct, idleFor, "the idle player must be named so their session can end")

	assert.Len(t, rules.appliedActions(), (MaxMissedTurns-1)*2)
}

func TestEngine_TurnTimeout_StaleTimerIsIgnored(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	// Capture the sequence the armed timer holds, then act: this is the player who
	// moved in the instant before their own clock ran out.
	engine.mu.Lock()
	stale := engine.clock.seq
	engine.mu.Unlock()

	acted := engine.CurrentPlayerID()
	require.NoError(t, engine.SubmitAction(acted, namedAction{name: "real"}))

	engine.onTurnTimeout(stale)

	assert.Equal(t, []string{acted + ":real"}, rules.appliedActions(),
		"a timeout for a turn that is over must not play anything")
	assert.Zero(t, engine.missedTurns(acted), "and must not be counted against them")
}

func TestEngine_TurnTimeout_NoSafeMoveTakesTheSeat(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: nil}
	engine := newTimeoutEngine(t, rules, "a", "b")

	fireTurnTimeout(t, engine)

	assert.Empty(t, rules.appliedActions(), "there was no safe move to play")
	assert.True(t, engine.IsFinished(), "the seat goes rather than the table waiting")
}

func TestEngine_TurnTimeout_RefusedSafeMoveRearmsRatherThanStalling(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}, reject: true}
	engine := newTimeoutEngine(t, rules, "a", "b")

	stuck := engine.CurrentPlayerID()
	fireTurnTimeout(t, engine)

	assert.Empty(t, rules.appliedActions(), "validation refused it")
	assert.Equal(t, 1, engine.missedTurns(stuck), "the miss still counts")
	assert.Equal(t, stuck, engine.CurrentPlayerID(), "the turn could not advance")
	assert.False(t, engine.turnDeadline().IsZero(),
		"a fresh clock must be running, or this seat stalls the table forever")
}

// Real timers under a fake clock: synctest runs every expiry the wait covers, so the
// check neither sleeps nor flakes under load.
func TestEngine_TurnTimeout_RefusedSafeMoveStillLosesTheSeat(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		rules := &timeoutRules{safe: namedAction{name: "safe"}, reject: true}
		engine := NewEngine(rules, []*Player{{ID: "a"}, {ID: "b"}},
			WithTurnTimeout(20*time.Millisecond))

		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		time.Sleep(MaxMissedTurns * time.Second)
		synctest.Wait()

		require.True(t, engine.IsFinished(), "each refusal must re-arm the clock until the idle seat is taken")
		assert.Empty(t, rules.appliedActions(), "nothing was ever accepted")
	})
}

func TestEngine_TurnTimeout_ClockLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("close stops the clock", func(t *testing.T) {
		t.Parallel()
		engine := newTimeoutEngine(t, &timeoutRules{safe: namedAction{name: "safe"}}, "a", "b")
		require.False(t, engine.turnDeadline().IsZero())

		engine.Close()

		assert.True(t, engine.turnDeadline().IsZero())
	})

	t.Run("a finished game stops the clock", func(t *testing.T) {
		t.Parallel()
		engine := newTimeoutEngine(t, &timeoutRules{safe: nil}, "a", "b")
		require.False(t, engine.turnDeadline().IsZero())

		fireTurnTimeout(t, engine)
		require.True(t, engine.IsFinished())

		assert.True(t, engine.turnDeadline().IsZero(),
			"a finished game must not auto-play")
	})

	t.Run("a zero timeout disables the clock", func(t *testing.T) {
		t.Parallel()
		rules := &timeoutRules{safe: namedAction{name: "safe"}}
		engine := NewEngine(rules, []*Player{{ID: "a"}, {ID: "b"}},
			WithTurnTimeout(0))

		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		assert.True(t, engine.turnDeadline().IsZero())
	})
}

func TestEngine_TurnTimeout_TimerActuallyFires(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const timeout = 20 * time.Millisecond
		rules := &timeoutRules{safe: namedAction{name: "safe"}}
		engine := NewEngine(rules, []*Player{{ID: "a"}, {ID: "b"}},
			WithTurnTimeout(timeout))

		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		time.Sleep(timeout - time.Millisecond)
		synctest.Wait()
		require.Empty(t, rules.appliedActions(), "nothing plays before the deadline")

		time.Sleep(time.Millisecond)
		synctest.Wait()
		assert.Len(t, rules.appliedActions(), 1, "the armed timer must play the safe move on its own, once")
	})
}

// Spamming moves the rules refuse must not read as presence. A rejected action
// clears nothing and settles no cursor, so clock.seq is unchanged and the seat still
// goes - otherwise a client could sit out forever by sending garbage once per expiry.
func TestEngine_TurnTimeout_RefusedMoveBeforeTheExpiryStillLosesTheSeat(t *testing.T) {
	t.Parallel()

	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	victim := engine.CurrentPlayerID()
	engine.mu.Lock()
	engine.clock.missed[victim] = MaxMissedTurns - 1
	seq := engine.clock.seq
	engine.mu.Unlock()

	// The refusal lands before the expiry takes the lock.
	rules.reject = true
	require.Error(t, engine.SubmitAction(victim, namedAction{name: "refused"}))
	require.Equal(t, MaxMissedTurns-1, engine.missedTurns(victim),
		"a refused move does not clear the count")

	engine.onTurnTimeout(seq)

	engine.WithState(func(state *State) {
		seated := false
		for _, p := range state.Players {
			if p.ID == victim {
				seated = true
				break
			}
		}
		assert.False(t, seated, "garbage input is not playing: the seat is taken")
	})
}

// A charged miss is announced before the move played for it, so a view can say why
// the turn moved on.
func TestEngine_TurnTimeout_EventShipsWithTheMiss(t *testing.T) {
	t.Parallel()

	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")
	// Subscribed after Start, so the feed holds nothing yet.
	events, err := engine.Subscribe()
	require.NoError(t, err)

	engine.mu.Lock()
	seq := engine.clock.seq
	engine.mu.Unlock()

	expired := engine.CurrentPlayerID()
	engine.onTurnTimeout(seq)

	select {
	case ev := <-events:
		assert.Equal(t, EventTurnTimedOut, ev.Type)
		assert.Equal(t, expired, ev.PlayerID)
	default:
		t.Fatal("the expiry was charged without being announced")
	}
}

// A seat that is taken for idling must not also be announced as merely slow: the view
// treats EventPlayerIdle as the end of the session, and a stray timed-out event before
// it would be rendered as a turn that is still in play.
func TestEngine_TurnTimeout_TakingTheSeatSkipsTheTimedOutEvent(t *testing.T) {
	t.Parallel()

	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	victim := engine.CurrentPlayerID()
	engine.mu.Lock()
	engine.clock.missed[victim] = MaxMissedTurns - 1
	seq := engine.clock.seq
	engine.mu.Unlock()

	events, err := engine.Subscribe()
	require.NoError(t, err)

	engine.onTurnTimeout(seq)

	var types []EventType
	for {
		select {
		case ev := <-events:
			types = append(types, ev.Type)
			continue
		default:
		}
		break
	}
	assert.NotContains(t, types, EventTurnTimedOut)
	assert.Contains(t, types, EventPlayerIdle)
	engine.WithState(func(state *State) {
		assert.False(t, slices.ContainsFunc(state.Players, func(p *Player) bool { return p.ID == victim }),
			"the seat was taken")
	})
}

// stretchedRules gives one phase of the game a longer clock, the way hearts does for
// its pass phase and poker for the between-hands deal.
type stretchedRules struct {
	*timeoutRules
	override time.Duration
}

func (r stretchedRules) TurnDuration(*State) time.Duration { return r.override }

func TestEngine_TurnTimeout_RulesCanStretchATurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override time.Duration
		wantMin  time.Duration
	}{
		{name: "a longer phase gets a longer clock", override: time.Hour, wantMin: 30 * time.Minute},
		{name: "zero keeps the engine default", override: 0, wantMin: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const engineDefault = time.Minute
			rules := stretchedRules{
				timeoutRules: &timeoutRules{safe: namedAction{name: "safe"}},
				override:     tt.override,
			}
			engine := NewEngine(rules, []*Player{{ID: "a"}, {ID: "b"}},
				WithTurnTimeout(engineDefault))

			t.Cleanup(engine.Close)
			require.NoError(t, engine.Start())

			remaining := time.Until(engine.turnDeadline())
			assert.Greater(t, remaining, tt.wantMin)
			assert.LessOrEqual(t, remaining, max(tt.override, engineDefault))
		})
	}
}

// A rules set can stretch a turn but cannot conjure a clock the engine was built
// without: WithTurnTimeout(0) is the operator's off switch.
func TestEngine_TurnTimeout_StretchCannotResurrectADisabledClock(t *testing.T) {
	t.Parallel()

	rules := stretchedRules{
		timeoutRules: &timeoutRules{safe: namedAction{name: "safe"}},
		override:     time.Hour,
	}
	engine := NewEngine(rules, []*Player{{ID: "a"}, {ID: "b"}},
		WithTurnTimeout(0))

	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	assert.True(t, engine.turnDeadline().IsZero())
}

// panickingRules is a rules bug in the one hook that runs on a timer goroutine.
type panickingRules struct{ *timeoutRules }

func (*panickingRules) TimeoutAction(*State) Action { panic("rules bug in auto-play") }

// A panic inside TimeoutAction unwinds a time.AfterFunc goroutine, and an unrecovered
// panic there kills the process: every table on the server, for one game's bug. The
// engine has to catch it and end just this table, the way it ends one on a rules
// error from SubmitAction.
func TestEngine_TurnTimeout_RulesPanicEndsOnlyThisTable(t *testing.T) {
	t.Parallel()

	rules := &panickingRules{&timeoutRules{safe: namedAction{name: "safe"}}}
	engine := newTimeoutEngine(t, rules, "a", "b")

	events, err := engine.Subscribe()
	require.NoError(t, err)
	engine.mu.Lock()
	seq := engine.clock.seq
	engine.mu.Unlock()

	require.NotPanics(t, func() { engine.onTurnTimeout(seq) }, "the timer goroutine must recover")

	assert.True(t, engine.IsFinished(), "the table with the buggy rules is over")
	var ended *Event
	for {
		select {
		case ev := <-events:
			if ev.Type == EventGameEnded {
				ended = &ev
			}
			continue
		default:
		}
		break
	}
	require.NotNil(t, ended, "the end is announced so the lobby can finalize")
	assert.Equal(t, EndReasonRulesError, ended.Reason, "a panic is a rules error: the match is not rated")
}

// The idle signal is what keeps the lobby's roster in step with the engine, so it is
// lossless and arrives once per taken seat. It runs after the engine lock is released,
// because the lobby's locks order before it.
func TestEngine_TurnTimeout_OnIdleRemovedNamesEachTakenSeat(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var (
			mu      sync.Mutex
			removed []string
		)
		var engine *Engine
		engine = NewEngine(&timeoutRules{safe: nil}, []*Player{{ID: "a"}, {ID: "b"}, {ID: "c"}},
			WithTurnTimeout(20*time.Millisecond), WithOnIdleRemoved(func(id string) {

				unlocked := engine.mu.TryLock()
				assert.True(t, unlocked, "the hook ran under the engine lock")
				if unlocked {
					engine.mu.Unlock()
				}
				mu.Lock()
				removed = append(removed, id)
				mu.Unlock()
			}))

		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		time.Sleep(time.Second)
		synctest.Wait()

		require.True(t, engine.IsFinished(), "two seats taken leaves one, who wins by forfeit")
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, removed, 2)
		assert.NotEqual(t, removed[0], removed[1])
	})
}

// RemovePlayer's callers (the lobby, a disconnect grace timer) may hold the lobby's
// locks, so a leave never reaches the idle hook.
func TestEngine_RemovePlayer_DoesNotCallOnIdleRemoved(t *testing.T) {
	t.Parallel()
	called := false
	engine := NewEngine(&timeoutRules{safe: namedAction{name: "safe"}}, []*Player{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		WithTurnTimeout(time.Hour), WithOnIdleRemoved(func(string) { called = true }))

	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	engine.RemovePlayer("a")

	assert.False(t, called)
}
