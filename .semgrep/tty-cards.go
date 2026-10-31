// Rule tests for tty-cards.yml, run by `semgrep --test .semgrep/`. The directory starts
// with a dot, so `go build ./...` and golangci-lint never see this file.
package semgreptest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

func turnCursor(state *State, extra *Extra) {
	// ruleid: turn-cursor-written-by-hand
	state.CurrentTurn = extra.Dealer
	// ruleid: turn-cursor-written-by-hand
	state.CurrentTurn++
	// ruleid: turn-cursor-written-by-hand
	state.CurrentTurn -= 1
	// ruleid: turn-cursor-written-by-hand
	state.OverrideNextTurn = &extra.Dealer
	// ruleid: turn-cursor-written-by-hand
	state.OverrideNextTurn = nil
	// ok: turn-cursor-written-by-hand
	state.SetTurn(extra.Dealer)
	// ok: turn-cursor-written-by-hand
	state.OverrideTurn(extra.Dealer)
	// ok: turn-cursor-written-by-hand
	if state.CurrentTurn == extra.Dealer {
		return
	}
}

func engineOwnedState(state *State, extra *Extra) {
	// ruleid: rules-write-engine-owned-state
	state.Phase = game.Finished
	// ruleid: rules-write-engine-owned-state
	state.Players = state.Players[1:]
	// ruleid: rules-write-engine-owned-state
	state.LeftPlayers = nil
	// ruleid: rules-write-engine-owned-state
	state.Rules = nil
	// ok: rules-write-engine-owned-state
	extra.Phase = PhaseHandOver
	// ok: rules-write-engine-owned-state
	if state.Phase == game.Playing {
		return
	}
}

func view(engine *Engine, bound *BoundEngine, id string, act Action) {
	// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
	engine.WithState(func(*State) {})
	// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
	_ = engine.SubmitAction(id, act)
	// ruleid: tui-uses-server-engine-api
	engine.Frame(id, func(*State) {})
	// ok: tui-uses-server-engine-api
	_ = bound.Submit(act)
	// ok: tui-uses-server-engine-api
	bound.Frame(func(*State) {})
}

// ok: tui-uses-server-engine-api, rules-package-reaches-the-engine
// A comment may name game.Engine; code in internal/tui and the rules packages may not.
type tableView struct {
	// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
	engine *game.Engine
	// ok: tui-uses-server-engine-api
	seat *game.BoundEngine
}

// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
func newTableView(engine *game.Engine) {}

func lobbyView(l *Lobby, ev Event, id string) {
	// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
	_ = game.Bind(l.engine, id)
	// ruleid: tui-uses-server-engine-api, rules-package-reaches-the-engine
	_ = game.NewEngine(nil, nil)
	// ruleid: tui-uses-server-engine-api
	_ = l.ActiveGame()
	// ok: tui-uses-server-engine-api
	_ = l.Seat(id)
	// ok: tui-uses-server-engine-api
	_ = ev.Seat(id)
}

// The calls a view may not make, a rules package may not make either; these are the
// ones it may.
func rulesHook(state *State) error {
	// ok: rules-package-reaches-the-engine
	state.SetTurn(0)
	// ok: rules-package-reaches-the-engine
	_ = []game.EngineOption{}
	// ok: rules-package-reaches-the-engine
	return game.ErrUnknownAction
}

func recovers(reg *registry, s Session) {
	// ok: recover-helper-not-directly-deferred
	defer reg.recoverSession(s)
	// ok: recover-helper-not-directly-deferred
	defer recoverSession(s)
	defer func() {
		// ruleid: recover-helper-not-directly-deferred
		reg.reportPanic(s)
	}()
	// ruleid: recover-helper-not-directly-deferred
	reg.recoverSession(s)
	// ok: recover-helper-not-directly-deferred
	reg.recordSessionPanic(s, nil)
}

func telemetry(span Span, s Session) {
	// ruleid: client-address-in-telemetry-attribute
	span.SetAttributes(attribute.String("peer", s.RemoteAddr().String()))
	// ruleid: client-address-in-telemetry-attribute
	span.SetAttributes(attribute.String("net", clientNet(s.RemoteAddr())))
	// ruleid: client-address-in-telemetry-attribute
	span.SetAttributes(attribute.String("client.address", "x"))
	// ruleid: client-address-in-telemetry-attribute
	span.SetAttributes(attribute.String("remote_addr", addrString(s.RemoteAddr())))
	// ok: client-address-in-telemetry-attribute
	span.SetAttributes(attribute.String("user", s.User()))
}

func sessionLogs(s Session) {
	// ruleid: routine-log-carries-remote-addr, game-logs-without-its-table
	slog.Info("ssh connect", "remote_addr", addrString(s.RemoteAddr()))
	// ruleid: routine-log-carries-remote-addr, game-logs-without-its-table
	slog.InfoContext(s.Context(), "ssh connect", "peer", s.RemoteAddr().String())
	// ruleid: routine-log-carries-remote-addr, game-logs-without-its-table
	slog.DebugContext(s.Context(), "ssh disconnect", slog.String("remote_addr", "x"))
	// ok: routine-log-carries-remote-addr
	// ruleid: game-logs-without-its-table
	slog.InfoContext(s.Context(), "ssh connect", "client_net", clientNet(s.RemoteAddr()))
	// ok: routine-log-carries-remote-addr
	// ruleid: game-logs-without-its-table
	slog.WarnContext(s.Context(), "rate limited", "remote_addr", addrString(s.RemoteAddr()))
}

// The calls above are bare too, which in a rules package would drop the lobby code
// and the game slug the table logger carries.
func rulesLog(state *State, extra *Extra) {
	// ruleid: game-logs-without-its-table
	slog.Warn("chips do not add up", "hand", extra.Hand)
	// ruleid: game-logs-without-its-table
	slog.Default().Error("bad deal", "hand", extra.Hand)
	// ok: game-logs-without-its-table
	state.Logger().Warn("chips do not add up", "hand", extra.Hand)
	// ok: game-logs-without-its-table
	state.Logger().Info("hand dealt", slog.Int("hand", extra.Hand))
}

func credentials(cfg *Config) error {
	dsn := cfg.DSN()
	// ruleid: database-credential-leaves-config, game-logs-without-its-table
	slog.Info("connecting", "dsn", dsn)
	// ruleid: database-credential-leaves-config
	log.Printf("db %s", cfg.DSN())
	// ruleid: database-credential-leaves-config
	fmt.Println(cfg.DBPassword.Reveal())
	// ok: database-credential-leaves-config
	fmt.Println(cfg.DBPassword)
	// ok: database-credential-leaves-config
	// ruleid: game-logs-without-its-table
	slog.Info("config loaded", "config", cfg)
	// ok: database-credential-leaves-config
	Connect(dsn)
	if cfg.DBPassword == "" {
		// ok: database-credential-leaves-config
		return errors.New("DB_PASSWORD is required")
	}
	if len(cfg.DBPassword) < 12 {
		// ok: database-credential-leaves-config
		return fmt.Errorf("DB_PASSWORD needs 12 characters, has %d", len(cfg.DBPassword))
	}
	// ruleid: database-credential-leaves-config
	return fmt.Errorf("connect %s: %w", dsn, errors.ErrUnsupported)
}

func subscribers(engine *Engine) {
	// ruleid: subscribe-error-discarded
	ch, _ := engine.Subscribe()
	// ruleid: subscribe-error-discarded
	ch, _ = engine.Subscribe()
	// ok: subscribe-error-discarded
	ch, err := engine.Subscribe()
	_, _ = ch, err
}

func sessionIdentity(g GlobalContext, user *User) {
	// ruleid: session-player-mapped-twice
	_ = g.LobbyManager.FindLobbyByPlayer(lobby.NewPlayer(user))
	// ok: session-player-mapped-twice
	_ = g.LobbyManager.FindLobbyByPlayer(g.Player)
}

func env() bool {
	// ruleid: boolean-env-compared-as-string
	if os.Getenv("PROXY_PROTOCOL") == "true" {
		return true
	}
	raw := os.Getenv("API_TRUST_PROXY")
	// ruleid: boolean-env-compared-as-string
	if strings.EqualFold(raw, "TRUE") {
		return true
	}
	v, ok := os.LookupEnv("ALLOW_X")
	// ruleid: boolean-env-compared-as-string
	if ok && v != "0" {
		return true
	}
	// ok: boolean-env-compared-as-string
	if os.Getenv("DB_DSN") != "" {
		return true
	}
	// ok: boolean-env-compared-as-string
	b, err := strconv.ParseBool(os.Getenv("PROXY_PROTOCOL"))
	return b && err == nil
}

func frames(a, b string) {
	// ruleid: tui-uses-lipgloss-join
	_ = lg.JoinVertical(lg.Center, a, b)
	// ruleid: tui-uses-lipgloss-join
	_ = lipgloss.JoinHorizontal(lipgloss.Top, a, b)
	// ok: tui-uses-lipgloss-join
	_ = styles.JoinVertical(lg.Center, a, b)
	// ok: tui-uses-lipgloss-join
	_ = styles.JoinHorizontal(lg.Top, a, b)
}

// A view is handed the live *game.State; only redacted reads of it may reach a screen.
func hiddenCards(m *model, state *game.State, snap game.StateSnapshot, self *game.Player, extra *Extra, i int) {
	m.Sync(func(state *game.State) {
		// ruleid: tui-reads-hidden-cards
		_ = len(state.Players[i].Cards)
	})
	// ruleid: tui-reads-hidden-cards
	_ = state.Deck.Len()
	// ruleid: tui-reads-hidden-cards
	_ = self.Cards
	// ruleid: tui-reads-hidden-cards
	_ = extra.PendingPasses
	// ok: tui-reads-hidden-cards
	_ = snap.Players
	// ok: tui-reads-hidden-cards
	_ = logic.SeatsFor(state, self.ID)
	// ok: tui-reads-hidden-cards
	_ = logic.ActionPassCards{Cards: nil}
	for _, left := range state.LeftPlayers {
		// ok: tui-reads-hidden-cards
		_ = left.Name
		// ruleid: tui-reads-hidden-cards
		_ = left.Cards
	}
}

// Shutdown waits on work through drain.Gate, never a WaitGroup closed over by hand.
func shutdownGate(ctx context.Context, wg *sync.WaitGroup, gate *drain.Gate, sess *Session) {
	done := make(chan struct{})
	// ruleid: shutdown-gate-rolled-by-hand
	go func() {
		wg.Wait()
		close(done)
	}()
	// ruleid: shutdown-gate-rolled-by-hand
	go func() {
		defer close(done)
		wg.Wait()
	}()
	// ok: shutdown-gate-rolled-by-hand
	_ = gate.Wait(ctx)
	// ok: shutdown-gate-rolled-by-hand
	go func() {
		_ = sess.Wait()
		close(done)
	}()
}
