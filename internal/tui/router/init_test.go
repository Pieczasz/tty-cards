package router

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingModel records how many times it was initialized.
type countingModel struct{ inits *int }

func (m countingModel) Init() tea.Cmd                       { *m.inits++; return nil }
func (m countingModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m countingModel) View() tea.View                      { return tea.NewView("counting") }

// The home view used to be built by the constructor - which runs its Init - and then
// initialized again from Router.Init. For a view holding a subscription that is two
// listener goroutines racing for one channel, and one of the two commands is dropped.
func TestRouter_InitArmsTheFirstViewExactlyOnce(t *testing.T) {
	t.Parallel()

	var inits int
	r := New(GlobalContext{})
	r.Register(RouteHome, func(GlobalContext, Payload) tea.Model { return countingModel{inits: &inits} })

	r.Init()

	assert.Equal(t, 1, inits)
	require.NotNil(t, r.active, "Init is what puts a view on screen")
	assert.Equal(t, RouteHome, r.activeKey)
}

// Navigating is the same contract: the view that arrives is initialized once.
func TestRouter_GotoArmsTheViewExactlyOnce(t *testing.T) {
	t.Parallel()

	var home, next int
	r := New(GlobalContext{})
	r.Register(RouteHome, func(GlobalContext, Payload) tea.Model { return countingModel{inits: &home} })
	r.Register("next", func(GlobalContext, Payload) tea.Model { return countingModel{inits: &next} })

	r.Init()
	r.Update(ChangeViewMsg{ViewName: "next"})

	assert.Equal(t, 1, home)
	assert.Equal(t, 1, next)
}
