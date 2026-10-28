package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"

	"charm.land/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// The errors a refused session is shown. They are written to an unauthenticated
// client, so none of them may say more than the client already knows.
var (
	// errNoPublicKey refuses a client that authenticated without a key.
	errNoPublicKey = errors.New("SSH key authentication is required")
	// errInternal stands in for a database failure while resolving the account.
	errInternal = errors.New("internal server error")
	// errRegistrationFailed stands in for any registration failure not mapped below.
	errRegistrationFailed = errors.New("registration failed")
	// errTooManyRegistrations refuses a *new* account, never a returning player.
	errTooManyRegistrations = errors.New("too many new accounts from your network; please try again later")
	// errNameUnavailable is what an unauthenticated client gets for a name that is
	// taken. Echoing db.ErrUsernameTaken turned the login banner into a "does this
	// account exist" oracle over every username. An invalid name is not folded into
	// it: that is a fixed rule, not a fact about other accounts, so the player is told
	// why. LoadOrRegisterUser logs the real cause either way.
	errNameUnavailable = errors.New("could not register that name; try another with ssh -l <name>")
)

// SessionFingerprint is the SHA256 fingerprint of the key the session authenticated
// with, which is the account's identity.
func SessionFingerprint(s ssh.Session) (string, error) {
	key := s.PublicKey()
	if key == nil {
		return "", errNoPublicKey
	}
	return gossh.FingerprintSHA256(key), nil
}

// LoadOrRegisterUser resolves a fingerprint to an account, registering one on first
// sight. allowRegister gates only that first sight - a returning player never spends
// its budget - and may be nil where registration needs no limit. When it allows, its
// refund undoes that spend if the account is not created. Auth deliberately knows
// nothing about how the budget is counted; it only asks.
func LoadOrRegisterUser(
	ctx context.Context, userRepo db.Authenticator, sshUsername, fingerprint string,
	allowRegister func() (bool, func()),
) (*db.User, error) {
	user, key, err := userRepo.LoadUserByFingerprint(ctx, fingerprint)
	if err != nil {
		slog.ErrorContext(ctx, "database error while authenticating user", "error", err)
		return nil, errInternal
	}

	if user != nil {
		var keyID uint
		if key != nil {
			keyID = key.ID
		}
		if err := userRepo.UpdateUserActivity(ctx, user.ID, keyID); err != nil {
			// Non-fatal: stale activity timestamps must not block a login.
			slog.WarnContext(ctx, "failed to update user activity", "error", err)
		}
		return user, nil
	}

	// Before the budget: a typo must not spend one of a network's registrations.
	if err := db.ValidateUsername(sshUsername); err != nil {
		return nil, fmt.Errorf("%w: %w; try another with ssh -l <name>", db.ErrInvalidUsername, err)
	}
	var refund func()
	if allowRegister != nil {
		ok, undo := allowRegister()
		if !ok {
			slog.WarnContext(ctx, "refused new account registration: network over its budget")
			return nil, errTooManyRegistrations
		}
		refund = undo
	}
	user, _, err = userRepo.RegisterUserWithSSHKey(ctx, sshUsername, fingerprint)
	if err != nil {
		if refund != nil {
			refund()
		}
		// A taken name or a key registered by a racing connection is the player's
		// situation, not a server failure.
		level := slog.LevelError
		if errors.Is(err, db.ErrUsernameTaken) || errors.Is(err, db.ErrKeyAlreadyRegistered) {
			level = slog.LevelInfo
		}
		slog.Log(ctx, level, "failed to register new user", "error", err)
		return nil, mapRegisterError(err)
	}
	return user, nil
}

func mapRegisterError(err error) error {
	switch {
	case errors.Is(err, db.ErrUsernameTaken):
		return errNameUnavailable
	case errors.Is(err, db.ErrKeyAlreadyRegistered):
		// Not an oracle: the key is the caller's own, so this tells them nothing they
		// could not find out by connecting again.
		return err
	default:
		return errRegistrationFailed
	}
}

// registrationBudget is what a new account spends, narrowest first: its /64, its /48
// (a free tunnel is 65,536 /64s) and the server as a whole, for an attacker with more
// networks than any per-network budget can see. Returning players spend none of it.
type registrationBudget struct {
	net, wide, global *ratelimit.SlidingWindow
}

func newRegistrationBudget(cfg *config.Config) registrationBudget {
	return registrationBudget{
		net:    ratelimit.New(cfg.RegistrationLimit, cfg.RegistrationWindow),
		wide:   ratelimit.New(cfg.RegistrationLimitWide, cfg.RegistrationWindow),
		global: ratelimit.New(cfg.RegistrationGlobalLimit, cfg.RegistrationWindow),
	}
}

// allowRegistration answers whether this network may mint another account. An
// address that cannot be keyed is refused: registration is the one path where
// admitting an unmeterable client is worse than turning a real player away.
func allowRegistration(ctx context.Context, budget registrationBudget, s ssh.Session) (bool, func()) {
	host, ok := clientHost(s.RemoteAddr())
	if !ok {
		return false, nil
	}
	spends := []struct {
		limiter *ratelimit.SlidingWindow
		key     string
		name    string
	}{
		{budget.net, ratelimit.NetKey(host), "ssh_register"},
		{budget.wide, ratelimit.WideKey(host), "ssh_register_wide"},
		{budget.global, "", "ssh_register_global"},
	}
	for i, spend := range spends {
		if spend.limiter.Allow(spend.key) {
			continue
		}
		// Refused, so the account is never made: the narrower budgets get back what
		// they just admitted, or a flood filling the global ceiling would also lock
		// every real player who retried out of their own network's budget.
		for _, admitted := range spends[:i] {
			admitted.limiter.Refund(admitted.key)
		}
		observability.RateLimitReject(ctx, spend.name)
		return false, nil
	}
	return true, func() {
		for _, spend := range spends {
			spend.limiter.Refund(spend.key)
		}
	}
}
