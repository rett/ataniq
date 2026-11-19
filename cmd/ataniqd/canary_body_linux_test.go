//go:build linux && ataniq_canary

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leaddaemon"
)

// TestReadBodyIsBounded: a body file is read to one byte more than the
// admission accepts and no further, whatever its size.
func TestReadBodyIsBounded(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if maxBodyRead != leadcanary.MaxBodyBytes+1 {
		t.Fatalf("the bound is %d, want one more than the admission's %d", maxBodyRead, leadcanary.MaxBodyBytes)
	}
	for name, tc := range map[string]struct{ size, want int }{
		"empty":           {0, 0},
		"short":           {40, 40},
		"the cap":         {leadcanary.MaxBodyBytes, leadcanary.MaxBodyBytes},
		"one over":        {leadcanary.MaxBodyBytes + 1, leadcanary.MaxBodyBytes + 1},
		"four times over": {leadcanary.MaxBodyBytes * 4, leadcanary.MaxBodyBytes + 1},
		"far over":        {4 << 20, leadcanary.MaxBodyBytes + 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readBody(write("body.txt", tc.size))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want || got != strings.Repeat("x", tc.want) {
				t.Fatalf("read %d bytes of %d, want %d", len(got), tc.size, tc.want)
			}
		})
	}
}

// TestCanaryInjectOversizeBodyIsRefusedByTheDaemon: the part of an oversize
// file that is read is enough for the daemon to refuse it as too long, and
// nothing more of the file is sent.
func TestCanaryInjectOversizeBodyIsRefusedByTheDaemon(t *testing.T) {
	f := newCanaryFixture(t)
	ta := canaryApp(t)
	done := startApp(t, ta, []string{"--canary", f.cfgPath}, f.socket())
	requests := fakeHolder(t, f.holder)
	body := filepath.Join(f.base, "body.txt")
	if err := os.WriteFile(body, []byte(strings.Repeat("x", leadcanary.MaxBodyBytes*4)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := canary(t, "inject", "--config", f.cfgPath, "--message-id", "msg-1", "--body-file", body)
	if code != leaddaemon.ExitRefused || out != "" || !strings.Contains(errOut, "body-too-long") {
		t.Fatalf("oversize body: %d %q %q", code, out, errOut)
	}
	if n := len(requests()); n != 0 {
		t.Fatalf("%d requests reached the holder", n)
	}
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
}

// TestCanaryInjectBodyFileThatIsAFIFOIsRefusedWithoutWaiting: a FIFO that no
// one writes to does not hold the command: the open does not wait for a writer
// and the handle is refused because it is not a regular file.
func TestCanaryInjectBodyFileThatIsAFIFOIsRefusedWithoutWaiting(t *testing.T) {
	f := newCanaryFixture(t)
	fifo := filepath.Join(f.base, "body.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		code        int
		out, errOut string
	}
	done := make(chan result, 1)
	ta := newTestApp(t, nil)
	go func() {
		code := ta.run([]string{"canary", "inject", "--config", f.cfgPath, "--message-id", "msg-1", "--body-file", fifo})
		done <- result{code, ta.out.String(), ta.err.String()}
	}()
	select {
	case r := <-done:
		if r.code != leaddaemon.ExitUsage || r.out != "" || !strings.Contains(r.errOut, "--body-file: is not a regular file") {
			t.Fatalf("a FIFO: %d %q %q", r.code, r.out, r.errOut)
		}
	case <-time.After(10 * time.Second):
		// Let the command go before the test ends: a writer ends its open.
		if fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
		t.Fatal("the command waits on a FIFO that has no writer")
	}

	// The same without the command around it.
	if _, err := readBody(fifo); err == nil || err.Error() != "is not a regular file" {
		t.Fatalf("readBody of a FIFO: %v", err)
	}
	// A folder and a name that is not there are refused as before.
	if _, err := readBody(f.base); err == nil {
		t.Fatal("a folder was read")
	}
	if _, err := readBody(filepath.Join(f.base, "none.txt")); err == nil || err.Error() != "cannot be opened" {
		t.Fatalf("no file: %v", err)
	}
}
