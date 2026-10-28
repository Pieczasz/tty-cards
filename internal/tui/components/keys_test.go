package components

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The hint line promises keys; if a handler stops taking one, the player is told to
// press something that does nothing. Each hint names exactly the keys its table takes.
func TestHints_NameTheKeysTheHandlersTake(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"left", "h", "right", "l"} {
		_, ok := HorizontalNudge(key)
		assert.True(t, ok, "%q is in HintMove, so it must nudge", key)
	}
	for _, name := range []string{"<-", "h", "->", "l"} {
		assert.Contains(t, HintMove, name)
	}
	assert.True(t, strings.HasPrefix(HintLeave, KeyEsc+":"), "HintLeave names the esc key")
}

func TestNudge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key     string
		wantH   int
		wantV   int
		wantHOK bool
		wantVOK bool
	}{
		{key: "left", wantH: -1, wantHOK: true},
		{key: "h", wantH: -1, wantHOK: true},
		{key: "right", wantH: 1, wantHOK: true},
		{key: "l", wantH: 1, wantHOK: true},
		{key: "up", wantV: -1, wantVOK: true},
		{key: "k", wantV: -1, wantVOK: true},
		{key: "down", wantV: 1, wantVOK: true},
		{key: "j", wantV: 1, wantVOK: true},
		{key: " "},
		{key: "space"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			h, hok := HorizontalNudge(tt.key)
			v, vok := VerticalNudge(tt.key)
			assert.Equal(t, tt.wantH, h)
			assert.Equal(t, tt.wantHOK, hok)
			assert.Equal(t, tt.wantV, v)
			assert.Equal(t, tt.wantVOK, vok)
		})
	}
}

func TestIsYes(t *testing.T) {
	t.Parallel()
	assert.True(t, IsYes("y"))
	assert.True(t, IsYes("Y"))
	assert.False(t, IsYes("n"))
	assert.False(t, IsYes("enter"))
}
