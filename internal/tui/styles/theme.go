// Package styles holds the per-session Theme, the only place colours are defined, and
// the layout arithmetic every screen shares: the capped box, padding, clamping and the
// cached figlet banners.
package styles

import (
	"image/color"

	lg "charm.land/lipgloss/v2"
)

// Theme is the whole color vocabulary of the UI, resolved for one terminal.
type Theme struct {
	Dark bool

	Text      color.Color
	TextMuted color.Color
	TextDim   color.Color

	Accent    color.Color
	Heading   color.Color
	AccentAlt color.Color

	Error   color.Color
	Success color.Color
	Warning color.Color

	Selection color.Color

	Border      color.Color
	BorderMuted color.Color

	CardFace color.Color
	CardBack color.Color
	SuitRed  color.Color
	SuitDark color.Color

	// Uno colors are four mutually distinct tones; SuitRed/SuitDark stay as
	// real playing-card convention and must not be repurposed.
	UnoRed    color.Color
	UnoYellow color.Color
	UnoGreen  color.Color
	UnoBlue   color.Color

	TurnFg color.Color
	TurnBg color.Color

	Chips      [4]color.Color
	Placements [3]color.Color

	Box                lg.Style
	Title              lg.Style
	Welcome            lg.Style
	SectionHeading     lg.Style
	ActionsText        lg.Style
	LobbyCode          lg.Style
	HostTag            lg.Style
	GuestTag           lg.Style
	PlayerItemSelected lg.Style
	ErrorText          lg.Style
	SuccessText        lg.Style
	Muted              lg.Style
	Dim                lg.Style
	Accented           lg.Style
	TurnName           lg.Style

	// CardFrame is the border a card-sized box wears: a rendered card, poker's
	// empty board slot and its face-down hole card all have to agree on it, or the
	// board jumps by a column when a card lands. Callers add the margins.
	CardFrame lg.Style
	// PickerBox and PickerCell are the modal grid picker (Crazy Eights' suits,
	// Uno's colours). PickerCell carries no colour: the caller sets the border and
	// foreground from whether the cell is under the cursor.
	PickerBox  lg.Style
	PickerCell lg.Style
	// MeldBox frames one Gin Rummy meld on the hand-over screen.
	MeldBox lg.Style
}

// NewTheme resolves the palette for a terminal background.
func NewTheme(isDark bool) Theme {
	pick := lg.LightDark(isDark)

	t := Theme{
		Dark: isDark,

		Text:      pick(lg.Color("#1A1A1A"), lg.Color("#FAFAFA")),
		TextMuted: pick(lg.Color("#4A4A4A"), lg.Color("#D4D4D4")),
		TextDim:   pick(lg.Color("#5F5F5F"), lg.Color("#B4B4B4")),

		Accent:    pick(lg.Color("#7A6100"), lg.Color("#FFD700")),
		Heading:   pick(lg.Color("#8A5200"), lg.Color("#FFB454")),
		AccentAlt: pick(lg.Color("#0A6E6E"), lg.Color("#6BE3E3")),

		Error:   pick(lg.Color("#B00020"), lg.Color("#FF9494")),
		Success: pick(lg.Color("#0F7A3D"), lg.Color("#6FD48A")),
		Warning: pick(lg.Color("#8A5200"), lg.Color("#FFB454")),

		Selection: pick(lg.Color("#7A6100"), lg.Color("#FFD700")),

		Border:      pick(lg.Color("#5F5F5F"), lg.Color("#949494")),
		BorderMuted: pick(lg.Color("#808080"), lg.Color("#9A9A9A")),

		CardFace: pick(lg.Color("#2A2A2A"), lg.Color("#EEEEEE")),
		CardBack: pick(lg.Color("#6A6A6A"), lg.Color("#8A8A8A")),
		SuitRed:  pick(lg.Color("#C0261F"), lg.Color("#FF9494")),
		SuitDark: pick(lg.Color("#1A1A1A"), lg.Color("#DDDDDD")),

		UnoRed:    pick(lg.Color("#C0261F"), lg.Color("#FF9494")),
		UnoYellow: pick(lg.Color("#7A6100"), lg.Color("#FFD700")),
		UnoGreen:  pick(lg.Color("#0F7A3D"), lg.Color("#6FD48A")),
		UnoBlue:   pick(lg.Color("#1F4FA8"), lg.Color("#A8C5FF")),

		TurnFg: pick(lg.Color("#FFFFFF"), lg.Color("#1A1A1A")),
		TurnBg: pick(lg.Color("#6B4F1D"), lg.Color("#E8D5A3")),

		Chips: [4]color.Color{
			pick(lg.Color("#2A2A2A"), lg.Color("#EDEDED")),
			pick(lg.Color("#1F4FA8"), lg.Color("#7FA8F5")),
			pick(lg.Color("#1E6B34"), lg.Color("#6FBF73")),
			pick(lg.Color("#A32020"), lg.Color("#E88A8A")),
		},
		Placements: [3]color.Color{
			pick(lg.Color("#7A6100"), lg.Color("#FFD700")),
			pick(lg.Color("#5A5A5A"), lg.Color("#C8C8C8")),
			pick(lg.Color("#8A4B14"), lg.Color("#E6B37A")),
		},
	}

	t.Box = lg.NewStyle().Border(lg.RoundedBorder()).BorderForeground(t.Border).Padding(1, 2)
	t.Title = lg.NewStyle().Bold(true).Foreground(t.Text)
	t.Welcome = lg.NewStyle().Bold(true).Foreground(t.AccentAlt)
	t.SectionHeading = lg.NewStyle().Bold(true).Foreground(t.Heading)
	t.ActionsText = lg.NewStyle().Foreground(t.TextMuted)
	t.LobbyCode = lg.NewStyle().Bold(true).Foreground(t.Heading)
	t.HostTag = lg.NewStyle().Bold(true).Foreground(t.Accent)
	t.GuestTag = lg.NewStyle().Foreground(t.TextMuted)
	t.PlayerItemSelected = lg.NewStyle().Foreground(t.Selection)
	t.ErrorText = lg.NewStyle().Foreground(t.Error)
	t.SuccessText = lg.NewStyle().Foreground(t.Success)
	t.Muted = lg.NewStyle().Foreground(t.TextMuted)
	t.Dim = lg.NewStyle().Foreground(t.TextDim)
	t.Accented = lg.NewStyle().Bold(true).Foreground(t.Accent)
	t.TurnName = lg.NewStyle().Bold(true).Foreground(t.TurnFg).Background(t.TurnBg).Padding(0, 1)

	t.CardFrame = lg.NewStyle().Border(lg.RoundedBorder()).BorderForeground(t.CardFace)
	t.PickerBox = lg.NewStyle().Border(lg.RoundedBorder()).BorderForeground(t.Selection).Padding(1, 2)
	t.PickerCell = lg.NewStyle().Border(lg.RoundedBorder()).Padding(0, 1)
	t.MeldBox = lg.NewStyle().Border(lg.RoundedBorder()).BorderForeground(t.BorderMuted).Padding(0, 1)

	return t
}
