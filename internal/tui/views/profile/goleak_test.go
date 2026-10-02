package profile

import (
	"testing"

	"go.uber.org/goleak"
)

// These tests build views that subscribe to engines and lobbies; a Close regression
// parks a listener goroutine, which no assertion here would catch.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
