// Package home is the landing screen every session starts on.
package home

import (
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

type model struct {
	global router.GlobalContext

	// The name banner, memoized: it is rendered without the shared banner cache, so
	// without this every frame would re-parse the figlet font.
	nameKey    nameBannerKey
	nameBanner string
}

type nameBannerKey struct {
	name          string
	width, height int
}

// New builds the home screen.
func New(global router.GlobalContext) tea.Model {
	return &model{global: global}
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		return m, router.Navigate(route)
	}
	return m, nil
}

func (m *model) View() tea.View {
	name := "Player"
	if m.global.User != nil {
		name = m.global.User.Username
	}

	g, theme := m.global, m.global.Theme
	footer := views.Footer(theme, nil)
	width := styles.InnerWidth(g.Width)
	// The title, the greeting and the name are one group, centred together: the
	// screen frame would pin the title to the top and float the rest in the middle.
	// Two gap lines, then the rest shared between the two banners under the title.
	title := styles.RenderFigureASCII("tty cards", width, styles.TitleHeightBudget(g.Height))
	rest := styles.AvailableContentHeight(g.Width, g.Height, "", footer) - lg.Height(title) - 2
	each := max(rest/2, 1)

	hero := styles.JoinVertical(lg.Center,
		theme.Title.Render(title),
		"",
		theme.Welcome.Render(styles.RenderFigureASCII("Welcome", width, each)),
		"",
		theme.Accented.Render(m.nameBannerFor(name, width, each)),
	)
	return tea.NewView(views.RenderCenteredLayout(g, "", hero, footer))
}

// nameBannerFor is the player's name as a figlet, rendered outside the shared banner
// cache: a player chooses their name, and a cache keyed on it would let any account
// mint entries.
func (m *model) nameBannerFor(name string, width, height int) string {
	key := nameBannerKey{name: name, width: width, height: height}
	if key != m.nameKey || m.nameBanner == "" {
		m.nameKey, m.nameBanner = key, styles.RenderFigureUncached(name, width, height)
	}
	return m.nameBanner
}
