package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Pieczasz/terminal-card/internal/db"

	"charm.land/ssh"
	cryptossh "golang.org/x/crypto/ssh"
)

var (
	ErrNoPublicKey        = errors.New("SSH key authentication is required")
	ErrInternal           = errors.New("internal server error")
	ErrRegistrationFailed = errors.New("registration failed")
	// ErrTooManyRegistrations refuses a *new* account, never a returning player.
	ErrTooManyRegistrations = errors.New("too many new accounts from your network; please try again later")
	// ErrNameUnavailable is what an unauthenticated client gets for a name that is
	// taken. Echoing db.ErrUsernameTaken turned the login banner into a "does this
	// account exist" oracle over every username. An invalid name is not folded into
	// it: that is a fixed rule, not a fact about other accounts, so the player is told
	// why. LoadOrRegisterUser logs the real cause either way.
	ErrNameUnavailable = errors.New("could not register that name; try another with ssh -l <name>")
)

func AuthenticateSession(s ssh.Session) (string, error) {
	publicKey := s.PublicKey()
	if publicKey == nil {
		return "", ErrNoPublicKey
	}
	return cryptossh.FingerprintSHA256(publicKey), nil
}

// LoadOrRegisterUser resolves a fingerprint to an account, registering one on first
// sight. allowRegister gates only that first sight - a returning player never spends
// its budget - and may be nil where registration needs no limit. Auth deliberately
// knows nothing about how the budget is counted; it only asks.
func LoadOrRegisterUser(
	ctx context.Context, userRepo db.UserRepository, sshUsername, fingerprint string,
	allowRegister func() bool,
) (*db.User, error) {
	user, key, err := userRepo.LoadUserByFingerprint(ctx, fingerprint)
	if err != nil {
		slog.ErrorContext(ctx, "database error while authenticating user", "error", err)
		return nil, ErrInternal
	}

	if user == nil {
		// Before the budget: a typo must not spend one of a network's registrations.
		if err := db.ValidateUsername(sshUsername); err != nil {
			return nil, fmt.Errorf("%w: %w; try another with ssh -l <name>", db.ErrInvalidUsername, err)
		}
		if allowRegister != nil && !allowRegister() {
			slog.WarnContext(ctx, "refused new account registration: network over its budget")
			return nil, ErrTooManyRegistrations
		}
		user, _, err = userRepo.RegisterUserWithKey(ctx, sshUsername, fingerprint)
		if err != nil {
			slog.ErrorContext(ctx, "failed to register new user", "error", err)
			return nil, mapRegisterError(err)
		}
	} else {
		if err := userRepo.UpdateUserActivity(ctx, user, key); err != nil {
			// Non-fatal: stale activity timestamps must not block a login.
			slog.WarnContext(ctx, "failed to update user activity", "error", err)
		}
	}

	return user, nil
}

func mapRegisterError(err error) error {
	switch {
	case errors.Is(err, db.ErrUsernameTaken):
		return ErrNameUnavailable
	case errors.Is(err, db.ErrKeyAlreadyRegistered):
		// Not an oracle: the key is the caller's own, so this tells them nothing they
		// could not find out by connecting again.
		return err
	default:
		return ErrRegistrationFailed
	}
}
