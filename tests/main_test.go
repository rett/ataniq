package tests

import (
	"os"
	"testing"
)

// TestMain runs the tests and then removes what the package built once for
// all of them: the originals of the run kit's real binaries (see
// canaryRealOriginals), which no single test owns.
func TestMain(m *testing.M) {
	code := m.Run()
	canaryRealCleanup()
	os.Exit(code)
}
