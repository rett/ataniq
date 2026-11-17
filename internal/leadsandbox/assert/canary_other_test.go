//go:build !linux

package assert

import "testing"

// testCanary skips: the canary is implemented on Linux only.
func testCanary(t *testing.T) *Canary {
	t.Helper()
	t.Skip("the canary is implemented on Linux only")
	return nil
}
