package webpush

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain fails the run when a goroutine is left running after the tests:
// every fake push service is closed and every connection released by then.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if left := leftGoroutines(5 * time.Second); left != "" {
			fmt.Fprintf(os.Stderr, "goroutines left running after the tests:\n%s\n", left)
			code = 1
		}
	}
	os.Exit(code)
}

// leftGoroutines waits up to wait for every goroutine but this one to end
// (runtime.Stack leaves out the runtime's own), and returns the stacks of
// those that did not.
func leftGoroutines(wait time.Duration) string {
	deadline := time.Now().Add(wait)
	for {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		var left []string
		for _, g := range strings.Split(string(buf), "\n\n") {
			if g != "" && !strings.Contains(g, "leftGoroutines") {
				left = append(left, g)
			}
		}
		if len(left) == 0 {
			return ""
		}
		if time.Now().After(deadline) {
			return strings.Join(left, "\n\n")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
