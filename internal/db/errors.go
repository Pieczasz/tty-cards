package db

import "errors"

var (
	ErrUsernameTaken        = errors.New("username already taken")
	ErrKeyAlreadyRegistered = errors.New("public key already registered")
	ErrInvalidUsername      = errors.New("invalid username")
	ErrUserNotFound         = errors.New("user not found")
)
