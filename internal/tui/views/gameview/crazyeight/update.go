package crazyeight

import (
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.Handle(msg, m.syncState, moves)
	return m, cmd
}

// Close comes from the embedded Session: the router replaces this view on navigation
// and the ssh layer closes it on disconnect, so the esc/enter paths are not enough.
var _ router.Closer = (*model)(nil)
