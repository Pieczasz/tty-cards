// Package testutil holds the fixtures tests across the server share: stable ids and
// seats, and (behind the integration tag) a migrated Postgres per test.
package testutil

import (
	"encoding/binary"

	"uuid"
)

// UID is a stable UUID for tests that used to use integer ids. The integer occupies
// the last 8 bytes, so UID(1) through UID(2^64-1) are distinct and UID(0) is uuid.Nil().
func UID[T ~byte | ~uint | ~uint64 | ~int](n T) uuid.UUID {
	var id uuid.UUID
	binary.BigEndian.PutUint64(id[8:], uint64(n))
	return id
}

// SeatID is the engine seat key lobby.NewPlayer would give UID(n): the UUID string,
// not the decimal that used to be users.id.
func SeatID[T ~byte | ~uint | ~uint64 | ~int](n T) string {
	return UID(n).String()
}
