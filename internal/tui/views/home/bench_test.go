package home

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
)

func BenchmarkHomeView_Render(b *testing.B) {
	m := &model{global: router.GlobalContext{
		User:  &db.User{ID: testutil.UID(1), Username: "alice"},
		Width: 120, Height: 40, Theme: styles.NewTheme(true),
	}}

	b.ReportAllocs()
	for b.Loop() {
		_ = m.View()
	}
}
