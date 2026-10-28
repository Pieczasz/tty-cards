package ssh

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
)

func TestCapSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"a small terminal is left alone", 80, 24, 80, 24},
		{"the cap itself", maxTermWidth, maxTermHeight, maxTermWidth, maxTermHeight},
		{"wide and short is capped on one axis", 300, 24, maxTermWidth, 24},
		{"a hostile pty-req", 2000, 600, maxTermWidth, maxTermHeight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w, h := capSize(tt.w, tt.h)
			assert.Equal(t, tt.wantW, w)
			assert.Equal(t, tt.wantH, h)
		})
	}
}

// A resize is a request, capped; every other message is none of this filter's business.
func TestCapWindowSize(t *testing.T) {
	t.Parallel()
	assert.Equal(t, tea.WindowSizeMsg{Width: maxTermWidth, Height: maxTermHeight},
		capWindowSize(tea.WindowSizeMsg{Width: 4_000_000, Height: 3_000_000}))
	assert.Equal(t, tea.WindowSizeMsg{Width: 100, Height: 30},
		capWindowSize(tea.WindowSizeMsg{Width: 100, Height: 30}))
	assert.Equal(t, tea.KeyPressMsg{Code: 'a'}, capWindowSize(tea.KeyPressMsg{Code: 'a'}))
}
