//go:build linux

package ptyhost

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/fakeagent"
)

// startUnread starts the fake agent and reads nothing from the host: the
// output it writes stays queued on the master. Cleanup stops and closes it.
func startUnread(t *testing.T, script string, mod ...func(*Config)) *Host {
	t.Helper()
	argv, env := fakeagent.Command(script)
	cfg := Config{Argv: argv, Env: env, Cols: 100, Rows: 30, NoCgroup: true}
	for _, m := range mod {
		m(&cfg)
	}
	h, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		h.Stop(0)
		h.Close()
	})
	return h
}

// waitQueued polls Queued until it is want.
func waitQueued(t *testing.T, h *Host, want int) {
	t.Helper()
	end := time.Now().Add(patience)
	for {
		n, err := h.Queued()
		if err != nil {
			t.Fatalf("Queued: %v", err)
		}
		if n == want {
			return
		}
		if n > want || time.Now().After(end) {
			t.Fatalf("Queued = %d, want %d", n, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueuedIsZeroOnAQuietChild(t *testing.T) {
	rep := filepath.Join(t.TempDir(), "report")
	h := startUnread(t, "report "+rep+"; wait")
	// The report file shows that the child is up and wrote nothing to the
	// terminal before it.
	end := time.Now().Add(patience)
	for {
		if _, err := fakeagent.ReadReport(rep); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the child did not report")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n, err := h.Queued(); n != 0 || err != nil {
		t.Errorf("Queued = %d, %v, want 0", n, err)
	}
}

func TestQueuedCountsUnreadBytesAndDoesNotConsumeThem(t *testing.T) {
	// Ten bytes, written raw so that no output processing changes the count.
	const text = "0123456789"
	h := startUnread(t, "raw; print 0x30313233343536373839; wait")
	waitQueued(t, h, len(text))
	// Asking again changes nothing.
	for i := 0; i < 3; i++ {
		if n, err := h.Queued(); n != len(text) || err != nil {
			t.Fatalf("Queued (call %d) = %d, %v, want %d", i, n, err, len(text))
		}
	}
	buf := make([]byte, 64)
	n, err := h.Read(buf)
	if err != nil || string(buf[:n]) != text {
		t.Fatalf("Read = %q, %v, want %q", buf[:n], err, text)
	}
	if n, err := h.Queued(); n != 0 || err != nil {
		t.Errorf("Queued after the Read = %d, %v, want 0", n, err)
	}
}

func TestQueuedAfterCloseIsAnError(t *testing.T) {
	h := startUnread(t, "wait")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := h.Queued(); err == nil || n != 0 {
		t.Errorf("Queued after Close = %d, %v, want an error", n, err)
	}
}

func TestWriteByWithAFarDeadlineBehavesAsWrite(t *testing.T) {
	h, tl := start(t, "raw; say READY; echo 5")
	tl.waitFor(t, "READY")
	if n, err := h.WriteBy([]byte("hello"), time.Now().Add(time.Hour)); n != 5 || err != nil {
		t.Fatalf("WriteBy = %d, %v", n, err)
	}
	tl.waitEOF(t)
	if got := rx(tl.bytes()); string(got) != "hello" {
		t.Errorf("the child read %q (output %q)", got, tl.bytes())
	}
	if n, err := h.WriteBy(nil, time.Time{}); n != 0 || err != nil {
		t.Errorf("empty WriteBy = %d, %v", n, err)
	}
	if c := h.Counters(); c != (Counters{}) {
		t.Errorf("counters %+v, want none", c)
	}
}

func TestWriteByAfterCloseIsAnError(t *testing.T) {
	h := startUnread(t, "wait")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := h.WriteBy([]byte("x"), time.Now().Add(time.Hour)); err == nil || n != 0 {
		t.Errorf("WriteBy after Close = %d, %v, want an error", n, err)
	}
}

// timedOut checks one failed write: the deadline error class, a prefix only,
// the message form, and no counter touched.
func timedOut(t *testing.T, what string, h *Host, n, size int, err error) {
	t.Helper()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("%s = %d, %v, want a deadline error", what, n, err)
	}
	if n < 0 || n >= size {
		t.Errorf("%s: n = %d of %d", what, n, size)
	}
	if !strings.HasPrefix(err.Error(), "ptyhost: write: ") || !strings.Contains(err.Error(), " bytes accepted: ") {
		t.Errorf("%s: error %q has another form than a timed-out Write", what, err)
	}
	if c := h.Counters(); c != (Counters{}) {
		t.Errorf("%s: counters %+v, want none", what, c)
	}
}

func TestWriteByFailsAtItsDeadlineWhenTheChildDoesNotRead(t *testing.T) {
	// The host's own timeout is far off; only the deadline can end the write.
	h, tl := start(t, "raw; say READY; wait", opts{mod: func(c *Config) { c.WriteTimeout = time.Minute }})
	tl.waitFor(t, "READY")
	in := make([]byte, 1<<20) // far more than the tty input buffer
	begin := time.Now()
	n, err := h.WriteBy(in, begin.Add(300*time.Millisecond))
	took := time.Since(begin)
	timedOut(t, "WriteBy", h, n, len(in), err)
	if took < 250*time.Millisecond || took > 10*time.Second {
		t.Errorf("WriteBy gave up after %v, want about 300ms", took)
	}

	// A timed-out Write on a host of the same make has the same class,
	// message form and counters.
	h2, tl2 := start(t, "raw; say READY; wait", opts{mod: func(c *Config) { c.WriteTimeout = 300 * time.Millisecond }})
	tl2.waitFor(t, "READY")
	n2, err2 := h2.Write(in)
	timedOut(t, "Write", h2, n2, len(in), err2)
}

func TestWriteByUsesTheHostTimeoutWhenThatIsEarlier(t *testing.T) {
	h, tl := start(t, "raw; say READY; wait", opts{mod: func(c *Config) { c.WriteTimeout = 300 * time.Millisecond }})
	tl.waitFor(t, "READY")
	in := make([]byte, 1<<20)
	begin := time.Now()
	n, err := h.WriteBy(in, begin.Add(time.Hour))
	took := time.Since(begin)
	timedOut(t, "WriteBy", h, n, len(in), err)
	if took < 250*time.Millisecond || took > 10*time.Second {
		t.Errorf("WriteBy gave up after %v, want about the host timeout of 300ms", took)
	}
}

func TestWriteByWithAPassedDeadlineWritesNothing(t *testing.T) {
	h, tl := start(t, "raw; say READY; echo 5")
	tl.waitFor(t, "READY")
	for _, d := range []time.Time{time.Now().Add(-time.Second), time.Now(), {}} {
		n, err := h.WriteBy([]byte("XXXXX"), d)
		if n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("WriteBy(deadline %v) = %d, %v, want 0 and a deadline error", d, n, err)
		}
	}
	// The host still writes, and the child saw none of the refused bytes.
	if n, err := h.WriteBy([]byte("hello"), time.Now().Add(time.Hour)); n != 5 || err != nil {
		t.Fatalf("WriteBy = %d, %v", n, err)
	}
	tl.waitEOF(t)
	if got := rx(tl.bytes()); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("the child read %q, want only hello", got)
	}
}
