//go:build integration

package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The pair count runs on every ranked finalize, so its window scan must be able to
// use idx_matches_ranked_created rather than read a veteran's whole history. Sequential
// scans are switched off so a toy table cannot hide which indexes the query can use.
func TestRecentRankedMatchesCanUseTheWindowIndex(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	query := gormDB.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var ids []uint
		return recentRankedMatches(tx, []uuid.UUID{testutil.UID(1), testutil.UID(2)}, time.Now()).
			Pluck("matches.id", &ids)
	})

	var plan []string
	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		return tx.Raw("EXPLAIN " + query).Scan(&plan).Error
	}))
	joined := strings.Join(plan, "\n")
	assert.Contains(t, joined, "idx_matches_ranked_created", "plan:\n%s", joined)
}
