package styles

// The external test package reaches the box and padding arithmetic, and the banner
// cache, through these.
var (
	BoxWidth      = boxWidth
	BoxHeight     = boxHeight
	PadHorizontal = padHorizontal
	PadVertical   = padVertical
)

// ResetFigureCacheForTest empties the banner cache, for the cache-size tests, which
// cannot observe a count they share with every other test in the package.
func ResetFigureCacheForTest() {
	figureCache.Clear()
}

func FigureCacheLenForTest() int {
	n := 0
	figureCache.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
