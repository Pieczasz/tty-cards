package catalog

import (
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A disconnect never runs a view's esc/enter paths, so Close is the only thing that
// releases the engine subscription. A view that skips it parks a listener goroutine
// and holds a broadcaster slot for every dropped player until the engine closes.
func TestAll_CloseReleasesTheEngineSubscription(t *testing.T) {
	t.Parallel()
	for _, entry := range All {
		for _, engineFirst := range []bool{false, true} {
			name := entry.Slug
			if engineFirst {
				name += "/after_the_engine_closed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				engine, view := seatedEngineAndView(t, entry, entry.Factory().MinPlayers(), 120, 50)
				require.Equal(t, 1, engine.SubscriberCount(), "the view subscribed on construction")
				closer, ok := view.(router.Closer)
				require.True(t, ok, "every game view is a router.Closer")
				listener, ok := view.(interface{ Listen() tea.Cmd })
				require.True(t, ok, "every game view embeds gameview.Session")

				// Park a listener exactly as the Bubble Tea runtime would. It is built
				// here, on this goroutine, because Close writes the session's feed.
				listen := listener.Listen()
				done := make(chan tea.Msg, 1)
				go func() { done <- listen() }()

				if engineFirst {
					engine.Close()
				}
				closer.Close()
				assert.Zero(t, engine.SubscriberCount(), "Close returns the subscriber slot")

				select {
				case msg := <-done:
					assert.Nil(t, msg, "unsubscribing closes the channel so the listener returns")
				case <-time.After(2 * time.Second):
					t.Fatal("the listener goroutine did not return after Close")
				}

				assert.NotPanics(t, closer.Close, "session teardown may follow a view that already exited")
				engine.Close()
				assert.NotPanics(t, closer.Close, "and may follow the engine going away")
			})
		}
	}
}
