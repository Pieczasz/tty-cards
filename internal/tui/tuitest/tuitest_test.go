package tuitest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A key a test presses has to be the key a view's switch matches, or the test drives
// a binding nobody can reach.
func TestKey_SpellsWhatItWasGiven(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"esc", "enter", "backspace", "tab", "up", "down", "left", "right", "pgup", "pgdown",
		"space", "ctrl+c", "a", "Y", "3", "?",
	} {
		assert.Equal(t, s, Key(s).String())
	}
}

func TestStripANSI(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "red", StripANSI("\x1b[31mred\x1b[0m"))
}
