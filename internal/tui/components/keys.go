package components

// Key names exactly as tea.KeyPressMsg.String() spells them. A view matches these and
// never the literal: the spacebar is "space", not " ", and a case on " " compiled and
// then never matched.
const (
	KeyEsc   = "esc"
	KeyEnter = "enter"
	KeySpace = "space"
)

// HintMove and HintLeave are the key hints every table view prints for the keys
// HorizontalNudge and the leave prompt handle. They are built from the same names, and
// TestHints_NameTheKeysTheHandlersTake keeps the two from drifting.
const (
	HintMove  = "<-/h: left | ->/l: right"
	HintLeave = "esc: leave"
)

// HorizontalNudge is the step a left/right key asks for: -1 for left or h, +1 for right
// or l. Every cursor in the TUI reads the arrows and the vi keys as the same thing, so
// they are one table rather than a pair of literals in each view.
func HorizontalNudge(key string) (delta int, ok bool) {
	switch key {
	case "left", "h":
		return -1, true
	case "right", "l":
		return +1, true
	}
	return 0, false
}

// VerticalNudge is HorizontalNudge for up/down and k/j.
func VerticalNudge(key string) (delta int, ok bool) {
	switch key {
	case "up", "k":
		return -1, true
	case "down", "j":
		return +1, true
	}
	return 0, false
}

// IsYes is the confirming key of a yes/no prompt, in either case, so a leave prompt
// answers the same at the table and in the lobby.
func IsYes(key string) bool {
	return key == "y" || key == "Y"
}
