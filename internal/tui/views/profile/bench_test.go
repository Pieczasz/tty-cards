package profile

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
)

func BenchmarkProfileView_Render(b *testing.B) {
	user := &db.User{ID: testutil.UID(1), Username: "alice"}
	for i := range 3 {
		user.Rankings = append(user.Rankings, db.Ranking{
			Elo: uint32(1500 + i*40), Game: db.Game{Name: fmt.Sprintf("Game%d", i)},
		})
	}
	history := make([]db.MatchParticipant, 0, 10)
	for i := range 10 {
		history = append(history, db.MatchParticipant{
			Placement: i%3 + 1, EloDelta: 12 - i,
			Match: db.Match{Ranked: i%2 == 0, Game: db.Game{Name: "Poker"}},
		})
	}
	// The filter lists are not optional: renderContent reads the current entry of
	// each for its filter line, so a literal without them panics where the real
	// view, built by New, never can.
	m := &model{
		global:        router.GlobalContext{Player: lobby.NewPlayer(user), Width: 120, Height: 40, Theme: styles.NewTheme(true)},
		userProfile:   user,
		history:       history,
		gameFilters:   []game.Module{{Name: filterAllGames}, {Name: "Poker", Slug: "poker"}},
		resultFilters: []string{filterAllResults, filterWins, filterLosses},
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}
