package poker

import (
	"testing"

	"go.uber.org/goleak"
)

// A Close or Unsubscribe regression parks a listener goroutine, which no assertion
// in these packages would catch.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
