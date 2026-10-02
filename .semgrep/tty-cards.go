// Rule tests for tty-cards.yml, run by `semgrep --test .semgrep/`. The directory starts
// with a dot, so `go build ./...` and golangci-lint never see this file.
package semgreptest

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"

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
	// ok: turn-cursor-written-by-hand
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

func view(engine *Engine, bound *BoundEngine, id string, act Action) {
	// ruleid: tui-uses-server-engine-api
	engine.WithState(func(*State) {})
	// ruleid: tui-uses-server-engine-api
	_ = engine.SubmitAction(id, act)
	// ruleid: tui-uses-server-engine-api
	engine.Frame(id, func(*State) {})
	// ok: tui-uses-server-engine-api
	_ = bound.Submit(act)
	// ok: tui-uses-server-engine-api
	bound.Frame(func(*State) {})
}

// ok: tui-uses-server-engine-api
// A comment may name game.Engine; code in internal/tui may not.
type tableView struct {
	// ruleid: tui-uses-server-engine-api
	engine *game.Engine
	// ok: tui-uses-server-engine-api
	seat *game.BoundEngine
}

// ruleid: tui-uses-server-engine-api
func newTableView(engine *game.Engine) {}

func lobbyView(l *Lobby, ev Event, id string) {
	// ruleid: tui-uses-server-engine-api
	_ = game.Bind(l.engine, id)
	// ruleid: tui-uses-server-engine-api
	_ = game.NewEngine(nil, nil, nil)
	// ruleid: tui-uses-server-engine-api
	_ = l.ActiveGame()
	// ok: tui-uses-server-engine-api
	_ = l.Seat(id)
	// ok: tui-uses-server-engine-api
	_ = ev.Seat(id)
}

func recovers(reg *registry, s Session) {
	// ok: recover-helper-not-directly-deferred
	defer reg.recoverSession(s)
	// ok: recover-helper-not-directly-deferred
	defer recoverRulesPanic()
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
	// ruleid: routine-log-carries-remote-addr
	slog.Info("ssh connect", "remote_addr", addrString(s.RemoteAddr()))
	// ruleid: routine-log-carries-remote-addr
	slog.InfoContext(s.Context(), "ssh connect", "peer", s.RemoteAddr().String())
	// ruleid: routine-log-carries-remote-addr
	slog.DebugContext(s.Context(), "ssh disconnect", slog.String("remote_addr", "x"))
	// ok: routine-log-carries-remote-addr
	slog.InfoContext(s.Context(), "ssh connect", "client_net", clientNet(s.RemoteAddr()))
	// ok: routine-log-carries-remote-addr
	slog.WarnContext(s.Context(), "rate limited", "remote_addr", addrString(s.RemoteAddr()))
}

func credentials(cfg *Config) error {
	dsn := cfg.DSN()
	// ruleid: database-credential-leaves-config
	slog.Info("connecting", "dsn", dsn)
	// ruleid: database-credential-leaves-config
	log.Printf("db %s", cfg.DSN())
	// ruleid: database-credential-leaves-config
	fmt.Println(cfg.DBPassword.Reveal())
	// ok: database-credential-leaves-config
	fmt.Println(cfg.DBPassword)
	// ok: database-credential-leaves-config
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
