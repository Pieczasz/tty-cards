package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"

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

// authTimeout bounds each database round trip a login makes, the same budget the
// TUI gives a view's request: a stalled database must not hold an unauthenticated
// connection, and its session slot, open until the idle timeout.
const authTimeout = 5 * time.Second

// registrationGate decides whether a first-time fingerprint may register. It returns
// the error the player is shown when not, and otherwise a refund (may be nil) that
// gives back what the gate spent, for when the registration then fails.
type registrationGate func() (refund func(), err error)

// LoadOrRegisterUser resolves a fingerprint to an account, registering one on first
// sight. allowRegister gates only that first sight - a returning player never spends
// its budget - and may be nil where registration needs no limit. Auth deliberately
// knows nothing about how the budget is counted; it only asks.
func LoadOrRegisterUser(
	ctx context.Context, userRepo db.Authenticator, sshUsername, fingerprint string,
	allowRegister registrationGate,
) (*db.User, error) {
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	user, key, err := userRepo.LoadUserByFingerprint(ctx, fingerprint)
	if err != nil {
		slog.ErrorContext(ctx, "database error while authenticating user", "error", err)
		return nil, errInternal
	}

	if user != nil {
		if err := userRepo.UpdateUserActivity(ctx, user, key); err != nil {
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
		if refund, err = allowRegister(); err != nil {
			slog.WarnContext(ctx, "refused new account registration", "error", err)
			return nil, err
		}
	}
	user, _, err = userRepo.RegisterUserWithSSHKey(ctx, sshUsername, fingerprint)
	if err != nil {
		// No account was made, so the attempt costs the network nothing: a taken name
		// would otherwise burn five registrations on five typos.
		if refund != nil {
			refund()
		}
		// A taken name, a rejected name or a key registered by a racing connection is the
		// player's situation, not a server failure.
		level := slog.LevelError
		if isPlayerRegisterError(err) {
			level = slog.LevelInfo
		}
		slog.Log(ctx, level, "failed to register new user", "error", err)
		return nil, mapRegisterError(err)
	}
	return user, nil
}

// isPlayerRegisterError is a registration failure that is the player's situation, not
// the server's: the name or the key is the problem.
func isPlayerRegisterError(err error) bool {
	return errors.Is(err, db.ErrUsernameTaken) || errors.Is(err, db.ErrKeyAlreadyRegistered) ||
		errors.Is(err, db.ErrInvalidUsername)
}

func mapRegisterError(err error) error {
	switch {
	case errors.Is(err, db.ErrUsernameTaken):
		return errNameUnavailable
	case errors.Is(err, db.ErrKeyAlreadyRegistered):
		// Not an oracle: the key is the caller's own, so this tells them nothing they
		// could not find out by connecting again.
		return err
	case errors.Is(err, db.ErrInvalidUsername):
		// The repository checks the same rule as the pre-check above; if the two ever
		// disagree the player still needs the reason, not "registration failed".
		return err
	default:
		return errRegistrationFailed
	}
}
