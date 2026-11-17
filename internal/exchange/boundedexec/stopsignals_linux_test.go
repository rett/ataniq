package boundedexec

import (
	"os"
	"syscall"
	"testing"
)

// TestStopSignals: the list holds interrupt, terminate and hang-up, and each
// call returns a slice of its own.
func TestStopSignals(t *testing.T) {
	got := StopSignals()
	want := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	if len(got) != len(want) {
		t.Fatalf("StopSignals() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("StopSignals()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	got[0] = nil
	if StopSignals()[0] != os.Interrupt {
		t.Error("a call changed the list of the next call")
	}
}
