package db

import "errors"

var (
	ErrUsernameTaken        = errors.New("username already taken, please choose another via ssh config")
	ErrKeyAlreadyRegistered = errors.New("public key already registered")
	ErrInvalidUsername      = errors.New("invalid username")
	ErrUserNotFound         = errors.New("user not found")
)
