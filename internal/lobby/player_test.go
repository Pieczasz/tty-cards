package lobby

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"

	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NewPlayer is the only place a db.User becomes a game.Player, and it sits on the
// SSH login path, in tui.New and releaseSession. Everything the lobby shows
// or sorts by rating reads the map it builds here, so the flattening is worth
// pinning directly rather than through a caller that can hand Ratings in ready-made.
func TestNewPlayer(t *testing.T) {
	t.Parallel()

	t.Run("flattens rankings by game slug", func(t *testing.T) {
		t.Parallel()

		p := NewPlayer(&db.User{ID: testutil.UID(7),
			Username: "alice",
			Rankings: []db.Ranking{
				{Game: db.Game{Slug: "uno", Name: "Uno"}, Elo: 1700},
				{Game: db.Game{Slug: "hearts", Name: "Hearts"}, Elo: 1200},
			},
		})

		require.NotNil(t, p)
		assert.Equal(t, testutil.UID(7).String(), p.ID)
		assert.Equal(t, testutil.UID(7), p.UserID)
		assert.Equal(t, "alice", p.Name)
		assert.Equal(t, map[string]uint32{"uno": 1700, "hearts": 1200}, p.Ratings, "the slug, so a rename moves nobody")
	})

	// A ranking whose Game association did not load has no slug to key on. Keeping it
	// would put a rating under "", a key belonging to no game.
	t.Run("drops a ranking with no game slug", func(t *testing.T) {
		t.Parallel()

		p := NewPlayer(&db.User{ID: testutil.UID(9),
			Username: "bob",
			Rankings: []db.Ranking{
				{Game: db.Game{}, Elo: 3000},
				{Game: db.Game{Slug: "uno", Name: "Uno"}, Elo: 1400},
			},
		})

		require.NotNil(t, p)
		assert.NotContains(t, p.Ratings, "", "a game with no slug must not become a rating key")
		assert.Equal(t, map[string]uint32{"uno": 1400}, p.Ratings)
	})

	t.Run("no rankings gives an empty map, not nil lookups", func(t *testing.T) {
		t.Parallel()

		p := NewPlayer(&db.User{ID: testutil.UID(3), Username: "carol"})

		require.NotNil(t, p)
		assert.Empty(t, p.Ratings)
		_, ok := p.Ratings["uno"]
		assert.False(t, ok)
	})

	t.Run("nil user", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, NewPlayer(nil))
	})
}
