package testutil

import (
	"context"

	"github.com/Pieczasz/tty-cards/internal/db"

	"uuid"
)

// NopMatches is a db.MatchRepository that records nothing, for a test that wants a
// lobby.Manager and does not care what a finished match writes.
type NopMatches struct{}

var _ db.MatchRepository = NopMatches{}

func (NopMatches) RecordCasualMatch(context.Context, db.GameRef, []uuid.UUID) error { return nil }

func (NopMatches) FinalizeRankedMatch(context.Context, db.GameRef, []uuid.UUID, []int) (map[uuid.UUID]uint32, error) {
	return map[uuid.UUID]uint32{}, nil
}

func (NopMatches) FinalizeInterruptedMatch(
	context.Context, db.GameRef, []uuid.UUID, []int, []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	return map[uuid.UUID]uint32{}, nil
}
