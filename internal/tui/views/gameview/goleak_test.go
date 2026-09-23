package gameview

import (
	"testing"

	"go.uber.org/goleak"
)

// The five game view packages all check this; the package that owns Session did not,
// which is where a Close or Unsubscribe regression would actually be introduced.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
