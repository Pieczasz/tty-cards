package lobby

import "errors"

// Refusals a caller can tell apart with errors.Is. The text is what the player reads.
var (
	ErrInvalidCode     = errors.New("invalid lobby code")
	ErrLobbyNotFound   = errors.New("lobby not found")
	ErrLobbyFull       = errors.New("this lobby is full")
	ErrLobbyClosed     = errors.New("lobby is closed")
	ErrAlreadyInLobby  = errors.New("player is already in a lobby")
	ErrNotInLobby      = errors.New("player not in lobby")
	ErrJoinRateLimited = errors.New("too many join attempts, please try again later")
	ErrGameInProgress  = errors.New("game is already in progress")
	// ErrNotLeader is wrapped with what the caller tried, which completes the sentence:
	// "only the leader can change settings".
	ErrNotLeader = errors.New("only the leader can")
)

var (
	errNoCardGame     = errors.New("card game is required")
	errNotAccepting   = errors.New("lobby is not accepting players")
	errSettingsLocked = errors.New("cannot change settings while a game is in progress")
	errHostNotInLobby = errors.New("host is not in a lobby")
	errKickSelf       = errors.New("cannot kick yourself")
	errKickInGame     = errors.New("cannot kick during a game")
	errTooManyPlayers = errors.New("too many players for this game")
)
