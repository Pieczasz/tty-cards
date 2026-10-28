package lobby

import (
	"errors"
	"fmt"
)

// ErrRefused matches, under errors.Is, every request the lobby turned down because of
// what the player asked rather than because something broke. A refusal's own text is
// what the player reads; anything else the lobby returns is a failure, which it does
// not log - the caller does, once.
var ErrRefused = errors.New("request refused")

// refusal is a request the player caused the lobby to turn down.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

func (r *refusal) Is(target error) bool { return target == ErrRefused }

func refuse(msg string) error { return &refusal{msg: msg} }

// refusalf is a refusal carrying numeric detail, a seat bound or a roster size.
func refusalf(format string, args ...any) error {
	return &refusal{msg: fmt.Sprintf(format, args...)}
}

var (
	errInvalidCode     = refuse("invalid lobby code")
	errLobbyNotFound   = refuse("lobby not found")
	errLobbyFull       = refuse("this lobby is full")
	errLobbyClosed     = refuse("lobby is closed")
	errAlreadyInLobby  = refuse("player is already in a lobby")
	errNotInLobby      = refuse("player not in lobby")
	errJoinRateLimited = refuse("too many join attempts, please try again later")
	errGameInProgress  = refuse("game is already in progress")
	// errNotLeader is wrapped with what the caller tried, which completes the sentence:
	// "only the leader can change settings".
	errNotLeader      = refuse("only the leader can")
	errNoCardGame     = refuse("card game is required")
	errUnknownGame    = refuse("no such game on this server")
	errNotAccepting   = refuse("lobby is not accepting players")
	errSettingsLocked = refuse("cannot change settings while a game is in progress")
	errHostNotInLobby = refuse("host is not in a lobby")
	errKickSelf       = refuse("cannot kick yourself")
	errKickInGame     = refuse("cannot kick during a game")
	// errServerRestarting refuses a game that would start while the server drains.
	errServerRestarting = refuse("the server is restarting; no new games can start")
)
