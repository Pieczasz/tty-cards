package router

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
)

type MockModel struct {
	handledMsg tea.Msg
}

func (m MockModel) Init() tea.Cmd { return nil }
func (m MockModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.handledMsg = msg
	return m, nil
}
func (m MockModel) View() tea.View { return tea.NewView("mock") }

func TestRouter_RegistrationAndRouting(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})

	r.Register("mock", func(GlobalContext, any) tea.Model {
		return MockModel{}
	})

	cmd := r.Goto("mock", nil)
	assert.Nil(t, cmd)
	assert.NotNil(t, r.active)
	assert.Equal(t, Route("mock"), r.activeKey)
}

func TestRouter_UpdatePropagation(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})

	r.Register("mock", func(GlobalContext, any) tea.Model {
		return MockModel{}
	})

	r.Goto("mock", nil)

	keyMsg := tea.KeyPressMsg{Code: rune("a"[0]), Text: "a"}
	newModel, _ := r.Update(keyMsg)

	assert.Equal(t, keyMsg, newModel.(*Router).active.(MockModel).handledMsg)
}

func TestRouter_WindowSize(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})

	sizeMsg := tea.WindowSizeMsg{Width: 100, Height: 200}
	newModel, _ := r.Update(sizeMsg)

	assert.Equal(t, 100, newModel.(*Router).Global.Width)
	assert.Equal(t, 200, newModel.(*Router).Global.Height)
}

func TestRouter_ChangeViewMsg(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})
	r.Register("mock", func(GlobalContext, any) tea.Model {
		return MockModel{}
	})

	msg := ChangeViewMsg{ViewName: "mock"}
	r.Update(msg)

	assert.Equal(t, Route("mock"), r.activeKey)
}

// closableModel records teardown so the router's release contract is observable.
type closableModel struct {
	closed *int
}

func (m closableModel) Init() tea.Cmd                       { return nil }
func (m closableModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m closableModel) View() tea.View                      { return tea.NewView("closable") }
func (m closableModel) Close()                              { *m.closed++ }

func TestRouter_ReleasesOutgoingView(t *testing.T) {
	t.Parallel()
	var closed int
	r := New(GlobalContext{})
	r.Register("first", func(GlobalContext, any) tea.Model { return closableModel{closed: &closed} })
	r.Register("second", func(GlobalContext, any) tea.Model { return MockModel{} })

	r.Goto("first", nil)
	assert.Zero(t, closed)

	// Navigating away must release the view that held the subscription.
	r.Goto("second", nil)
	assert.Equal(t, 1, closed)

	// A view that holds nothing is simply skipped.
	r.Close()
	assert.Equal(t, 1, closed)
}

func TestRouter_CloseReleasesActiveView(t *testing.T) {
	t.Parallel()
	var closed int
	r := New(GlobalContext{})
	r.Register("first", func(GlobalContext, any) tea.Model { return closableModel{closed: &closed} })
	r.Goto("first", nil)

	r.Close()
	assert.Equal(t, 1, closed, "session teardown releases the active view")

	r.Close()
	assert.Equal(t, 1, closed, "Close is idempotent")
}

// An unknown route must not tear down the view the user is still looking at.
func TestRouter_UnknownRouteKeepsActiveView(t *testing.T) {
	t.Parallel()
	var closed int
	r := New(GlobalContext{})
	r.Register("first", func(GlobalContext, any) tea.Model { return closableModel{closed: &closed} })
	r.Goto("first", nil)

	assert.Nil(t, r.Goto("nope", nil))
	assert.Zero(t, closed)
}
