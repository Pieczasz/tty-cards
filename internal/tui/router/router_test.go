package router

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
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

	r.Register("mock", func(GlobalContext, Payload) tea.Model {
		return MockModel{}
	})

	cmd := r.Goto("mock", Payload{})
	assert.Nil(t, cmd)
	assert.NotNil(t, r.active)
	assert.Equal(t, Route("mock"), r.activeKey)
}

func TestRouter_UpdatePropagation(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})

	r.Register("mock", func(GlobalContext, Payload) tea.Model {
		return MockModel{}
	})

	r.Goto("mock", Payload{})

	keyMsg := tuitest.Key("a")
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

// A reseated player has to reach the views built later through the router's copy,
// and the one on screen through the message itself.
func TestRouter_UpdatePlayerStoresAndForwards(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})
	var built []*game.Player
	r.Register("mock", func(g GlobalContext, _ Payload) tea.Model {
		built = append(built, g.Player)
		return MockModel{}
	})
	r.Goto("mock", Payload{})
	p := &game.Player{ID: "p1", Ratings: map[string]uint32{"poker": 1516}}

	msg := UpdatePlayer(p)()
	r.Update(msg)
	assert.Equal(t, msg, r.active.(MockModel).handledMsg, "the view on screen never heard")
	r.Goto("mock", Payload{})

	assert.Same(t, p, r.Global.Player)
	assert.Equal(t, []*game.Player{nil, p}, built)
}

func TestRouter_ChangeViewMsg(t *testing.T) {
	t.Parallel()
	r := New(GlobalContext{})
	r.Register("mock", func(GlobalContext, Payload) tea.Model {
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
	r.Register("first", func(GlobalContext, Payload) tea.Model { return closableModel{closed: &closed} })
	r.Register("second", func(GlobalContext, Payload) tea.Model { return MockModel{} })

	r.Goto("first", Payload{})
	assert.Zero(t, closed)

	// Navigating away must release the view that held the subscription.
	r.Goto("second", Payload{})
	assert.Equal(t, 1, closed)

	// A view that holds nothing is simply skipped.
	r.Close()
	assert.Equal(t, 1, closed)
}

func TestRouter_CloseReleasesActiveView(t *testing.T) {
	t.Parallel()
	var closed int
	r := New(GlobalContext{})
	r.Register("first", func(GlobalContext, Payload) tea.Model { return closableModel{closed: &closed} })
	r.Goto("first", Payload{})

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
	r.Register("first", func(GlobalContext, Payload) tea.Model { return closableModel{closed: &closed} })
	r.Goto("first", Payload{})

	assert.Nil(t, r.Goto("nope", Payload{}))
	assert.Zero(t, closed)
}

// Navigate and its typed siblings are how every view moves the session: the command
// has to carry the route and its payload to the router unchanged, and the router has to
// hand that payload to the factory it builds.
func TestNavigate_RoutesThroughTheRouter(t *testing.T) {
	t.Parallel()
	table := &lobby.Lobby{}
	seat := game.Bind(&game.Engine{}, "7")

	tests := []struct {
		name  string
		cmd   tea.Cmd
		route Route
		want  Payload
	}{
		{name: "no payload", cmd: Navigate(RouteHome), route: RouteHome},
		{name: "a lobby", cmd: NavigateToLobby(table), route: RouteLobby, want: Payload{Lobby: table}},
		{name: "a seat", cmd: NavigateToGame("uno", seat), route: GameRoute("uno"), want: Payload{Table: seat}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got Payload
			r := New(GlobalContext{})
			r.Register(tt.route, func(_ GlobalContext, p Payload) tea.Model { got = p; return MockModel{} })

			msg := tt.cmd()

			assert.Equal(t, ChangeViewMsg{ViewName: tt.route, Payload: tt.want}, msg)
			r.Update(msg)
			assert.Equal(t, tt.route, r.activeKey)
			assert.Equal(t, tt.want, got)
		})
	}
}
