package uno

import (
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.Handle(msg, m.syncState, moves)
	return m, cmd
}

var _ router.Closer = (*model)(nil)
