package gameview

import (
	"image/color"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
)

// Choice is one cell of a ChoicePicker: its label, the suit it stands for and, when
// set, the tone the cell is drawn in.
type Choice struct {
	Label string
	Suit  deck.Suit
	Tone  func(styles.Theme) color.Color
}

// ChoicePicker is the modal grid a wild card opens over the table to ask which suit
// (crazy eights) or colour (uno) it becomes. Each choice is one entry, so the cell
// under the cursor cannot mean a different suit from the one on screen.
type ChoicePicker struct {
	Title   string
	Choices []Choice
	Open    bool
	Cursor  int
}

// Show opens the picker on its first choice.
func (p *ChoicePicker) Show() {
	p.Open, p.Cursor = true, 0
}

// Step moves the cursor by dx along a row and dy between rows, and reports whether
// the picker was open to take the key.
func (p *ChoicePicker) Step(dx, dy int) bool {
	if !p.Open {
		return false
	}
	p.Cursor = components.GridStep(p.Cursor, len(p.Choices), dx, dy)
	return true
}

// Pick closes the picker on the choice under the cursor. A cursor off the grid picks
// nothing and leaves the picker open, since there is nothing to commit.
func (p *ChoicePicker) Pick() (deck.Suit, bool) {
	if p.Cursor < 0 || p.Cursor >= len(p.Choices) {
		return deck.NoSuit, false
	}
	p.Open = false
	return p.Choices[p.Cursor].Suit, true
}

// Render draws the open picker, or nothing while it is closed.
func (p *ChoicePicker) Render(t styles.Theme) string {
	if !p.Open {
		return ""
	}
	labels := make([]string, 0, len(p.Choices))
	var tones []color.Color
	for _, c := range p.Choices {
		labels = append(labels, c.Label)
		if c.Tone != nil {
			tones = append(tones, c.Tone(t))
		}
	}
	return components.GridPicker{Title: p.Title, Labels: labels, Colors: tones, Cursor: p.Cursor}.Render(t)
}
