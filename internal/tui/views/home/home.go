package home

import (
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

type model struct {
	global router.GlobalContext
}

func New(global router.GlobalContext) tea.Model {
	return model{global: global}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	// q quits rather than going "back": home is where back leads.
	if key.String() == "q" {
		return m, tea.Quit
	}
	if route, ok := views.GlobalRoute(key.String()); ok {
		return m, router.Navigate(route, nil)
	}
	return m, nil
}

func (m model) View() tea.View {
	welcomeName := "Player"
	if m.global.User != nil {
		welcomeName = m.global.User.Username
	}

	return tea.NewView(views.RenderScreen(m.global, "Terminal Cards", nil, func(height int) string {
		// Only the fixed word is drawn as a figlet. The banner cache is keyed on its
		// text, so baking the username into it let any account mint cache entries:
		// enough of them fill the cap and every real screen title then re-parses the
		// whole figlet font on every frame. The name is styled text instead.
		//
		// The banner is the whole content, so it may use every line it is handed bar
		// the one the name sits on.
		banner := styles.RenderFigureASCII("Welcome", styles.InnerWidth(m.global.Width), max(height-1, 1))
		return lg.JoinVertical(lg.Center,
			m.global.Theme.Welcome.Render(banner),
			m.global.Theme.Accented.Render(welcomeName),
		)
	}))
}
