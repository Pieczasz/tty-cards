package styles_test

import (
	"fmt"
	"image/color"
	"math"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	"github.com/stretchr/testify/assert"
)

// Terminals do not report a single canonical background, so a token is held to
// its contrast ratio against every background a real user is plausibly running.
// Anything that passes against all of these passes everywhere in practice.
var (
	darkBackgrounds = map[string]string{
		"black":          "#000000",
		"vscode dark":    "#1E1E1E",
		"solarized dark": "#002B36",
		"one dark":       "#282C34",
		"nord":           "#2E3440",
		"cobalt":         "#193549",
		"deep blue":      "#1E3A8A",
	}
	lightBackgrounds = map[string]string{
		"white":           "#FFFFFF",
		"solarized light": "#FDF6E3",
		"paper":           "#F5F5F5",
		"grey":            "#EEEEEE",
	}
)

const (
	wcagMinBodyContrast   = 4.5
	wcagMinObjectContrast = 3.0
)

func TestTheme_TextContrast(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode        string
		isDark      bool
		backgrounds map[string]string
	}{
		{mode: "dark", isDark: true, backgrounds: darkBackgrounds},
		{mode: "light", isDark: false, backgrounds: lightBackgrounds},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			theme := styles.NewTheme(tc.isDark)

			text := map[string]color.Color{
				"Text":      theme.Text,
				"TextMuted": theme.TextMuted,
				"TextDim":   theme.TextDim,
				"Accent":    theme.Accent,
				"Heading":   theme.Heading,
				"AccentAlt": theme.AccentAlt,
				"Error":     theme.Error,
				"Success":   theme.Success,
				"Warning":   theme.Warning,
				"Selection": theme.Selection,
				"SuitRed":   theme.SuitRed,
				"SuitDark":  theme.SuitDark,
				"UnoRed":    theme.UnoRed,
				"UnoYellow": theme.UnoYellow,
				"UnoGreen":  theme.UnoGreen,
				"UnoBlue":   theme.UnoBlue,
				"CardFace":  theme.CardFace,
			}
			for i, c := range theme.Placements {
				text[fmt.Sprintf("Placements[%d]", i)] = c
			}

			for name, fg := range text {
				for bgName, bg := range tc.backgrounds {
					got := contrastRatio(fg, hex(bg))
					assert.GreaterOrEqual(t, got, wcagMinBodyContrast,
						"%s on %s (%s) is %.2f:1, below WCAG AA for text", name, bgName, bg, got)
				}
			}
		})
	}
}

func TestTheme_ObjectContrast(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode        string
		isDark      bool
		backgrounds map[string]string
	}{
		{mode: "dark", isDark: true, backgrounds: darkBackgrounds},
		{mode: "light", isDark: false, backgrounds: lightBackgrounds},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			theme := styles.NewTheme(tc.isDark)

			objects := map[string]color.Color{
				"Border":      theme.Border,
				"BorderMuted": theme.BorderMuted,
				"CardBack":    theme.CardBack,
			}
			for i, c := range theme.Chips {
				objects[fmt.Sprintf("Chips[%d]", i)] = c
			}

			for name, fg := range objects {
				for bgName, bg := range tc.backgrounds {
					got := contrastRatio(fg, hex(bg))
					assert.GreaterOrEqual(t, got, wcagMinObjectContrast,
						"%s on %s (%s) is %.2f:1, below WCAG AA for non-text", name, bgName, bg, got)
				}
			}
		})
	}
}

func TestTheme_TurnBadgeContrastsWithItsOwnBackground(t *testing.T) {
	t.Parallel()

	for _, isDark := range []bool{true, false} {
		theme := styles.NewTheme(isDark)
		got := contrastRatio(theme.TurnFg, theme.TurnBg)
		assert.GreaterOrEqual(t, got, wcagMinBodyContrast,
			"turn badge (dark=%v) is %.2f:1", isDark, got)
	}
}

func TestNewTheme_PicksOppositeEndsOfThePalette(t *testing.T) {
	t.Parallel()

	dark := styles.NewTheme(true)
	light := styles.NewTheme(false)

	assert.True(t, dark.Dark)
	assert.False(t, light.Dark)
	assert.NotEqual(t, dark.Text, light.Text, "the same token must differ between modes")
	assert.Greater(t, relativeLuminance(dark.Text), relativeLuminance(light.Text),
		"body text is light on a dark terminal and dark on a light one")
}

func hex(s string) color.Color {
	var r, g, b uint8
	if _, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b); err != nil {
		panic("bad hex in test: " + s)
	}
	return color.RGBA{R: r, G: g, B: b, A: 0xFF}
}

func relativeLuminance(c color.Color) float64 {
	r16, g16, b16, _ := c.RGBA()
	channel := func(v uint32) float64 {
		s := float64(v) / 0xFFFF
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r16) + 0.7152*channel(g16) + 0.0722*channel(b16)
}

func contrastRatio(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	lighter, darker := max(la, lb), min(la, lb)
	return (lighter + 0.05) / (darker + 0.05)
}
