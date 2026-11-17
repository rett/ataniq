//go:build linux

package sandboxnet

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHostFSFIFODoesNotBlock checks that a FIFO with no writer is refused and
// does not make the read wait. The FIFO is made under a temporary directory.
func TestHostFSFIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := HostFS{}.ReadFile(fifo, 10)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO was read")
		}
	case <-time.After(10 * time.Second): // a guard against a hang, not a synchronisation
		t.Fatal("ReadFile blocked on a FIFO")
	}
}
