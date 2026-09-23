// Package tuitest holds the helpers the TUI test suites share: keystrokes, escape
// stripping and the terminal sizes every full-screen view must fit.
package tuitest

import (
	"strings"
	"unicode/utf8"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// namedKeys are the keys that carry a code and no text. Building one from its first
// letter would turn "esc" into the letter 'e'.
var namedKeys = map[string]rune{
	"esc":       tea.KeyEscape,
	"enter":     tea.KeyEnter,
	"backspace": tea.KeyBackspace,
	"tab":       tea.KeyTab,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
}

// Key is the message Bubble Tea delivers for the key s names, spelled the way
// KeyPressMsg.String() spells it: "esc", "space", "ctrl+c", or one character.
func Key(s string) tea.KeyPressMsg {
	if code, ok := namedKeys[s]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	if s == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if letter, ok := strings.CutPrefix(s, "ctrl+"); ok {
		r, _ := utf8.DecodeRuneInString(letter)
		return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
	}
	r, _ := utf8.DecodeRuneInString(s)
	return tea.KeyPressMsg{Code: r, Text: s}
}

// StripANSI is s without its escape sequences, which is what a test compares.
func StripANSI(s string) string {
	return ansi.Strip(s)
}

// Size is a terminal a view is rendered into.
type Size struct {
	Name          string
	Width, Height int
}

// FitSizes are the terminals every screen has to fit: the declared minimum, the
// commonest terminal there is, and a tall one.
var FitSizes = []Size{
	{Name: "the declared minimum", Width: styles.MinWidth, Height: styles.MinHeight},
	{Name: "a stock terminal", Width: 80, Height: 24},
	{Name: "a tall terminal", Width: 120, Height: 50},
}
