//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// message is a canary message as the fence pastes it: a header line, a line
// break and a body with the token line.
func message(extra string) string {
	return "[ataniq-msg id=7 from=ataniq kind=canary]\ncanary-token: " + testToken + "\n" + extra
}

func (r testRepo) committed(t testing.TB) bool {
	return runGit(t, r.Dir, "rev-parse", "--abbrev-ref", "HEAD") == "canary/one"
}

func TestPlainDrawsAPromptAndTurnsBracketedPasteOn(t *testing.T) {
	r := newRepo(t)
	run := startRun(t, r, 0)
	i := run.pty.waitFor(t, 0, "> ")
	run.pty.waitFor(t, i-2, "\x1b[?2004h")
	if bytes.Contains(run.pty.output(), []byte("7501")) {
		t.Fatal("plain mode wrote an OSC 7501 query")
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestPlainSubmittedMessageCommitsTheToken(t *testing.T) {
	r := newRepo(t)
	first := r.head(t)
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, "\x1b[?2004h")
	// Typed keys are echoed and change nothing.
	run.pty.send(t, "hello")
	off := run.pty.waitFor(t, 0, "hello")
	run.pty.send(t, pasteOf(message("")))
	run.pty.send(t, "\r")
	run.pty.waitFor(t, off, "committed canary/one")
	waitUntil(t, "the commit", func() bool { return r.committed(t) })

	b, err := os.ReadFile(filepath.Join(r.Dir, "canary-token.txt"))
	if err != nil || string(b) != testToken+"\n" {
		t.Fatalf("file %q, %v", b, err)
	}
	if got := runGit(t, r.Dir, "log", "-1", "--format=%s"); got != "canary: token" {
		t.Fatalf("subject %q", got)
	}
	if got := runGit(t, r.Dir, "rev-parse", "HEAD^"); got != first {
		t.Fatalf("parent %s, want %s", got, first)
	}
	if got := runGit(t, r.Dir, "rev-parse", "develop"); got != first {
		t.Fatalf("develop moved to %s", got)
	}
	// The pasted escape sequences were not passed on to the screen.
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestPlainChangesNothingWithoutASubmittedToken(t *testing.T) {
	for _, c := range []struct {
		name, input string
	}{
		{"typed keys and a CR", "hello\r"},
		{"a typed token line and a CR", "canary-token: " + testToken + "\r"},
		{"a paste without a token line", pasteOf("hello\nworld") + "\r"},
		{"a paste with a bad token", pasteOf("canary-token: Short\n") + "\r"},
		{"a token paste not submitted", pasteOf(message(""))},
		{"a token paste and typed keys", pasteOf(message("")) + "xyz"},
		{"a CR before the paste", "\r" + pasteOf(message(""))},
		{"a CR inside the paste only", pasteOf(message("") + "\r")},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			first := r.head(t)
			run := startRun(t, r, 0)
			run.pty.waitFor(t, 0, "\x1b[?2004h")
			run.pty.send(t, c.input)
			// A marker typed after the input proves the stand-in read it all.
			run.pty.send(t, "ZZMARK")
			run.pty.waitFor(t, 0, "ZZMARK")
			time.Sleep(100 * time.Millisecond)
			if err := run.finish(t); err != nil {
				t.Fatal(err)
			}
			if r.head(t) != first || r.branch(t) != "develop" {
				t.Fatal("the repository changed")
			}
			if _, err := os.Stat(filepath.Join(r.Dir, "canary-token.txt")); err == nil {
				t.Fatal("the token file exists")
			}
		})
	}
}

// A message whose body holds shell text runs nothing: the token is the only
// thing taken from it.
func TestPlainRunsNoTextFromTheMessage(t *testing.T) {
	r := newRepo(t)
	pwned := filepath.Join(t.TempDir(), "pwned")
	body := strings.Join([]string{
		"touch " + pwned,
		"$(touch " + pwned + ")",
		"`touch " + pwned + "`",
		"x; touch " + pwned + " #",
		"canary-token: " + testToken + "; touch " + pwned,
		"| sh -c 'touch " + pwned + "'",
		"\x1b]0;$(touch " + pwned + ")\x07",
	}, "\r")
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, "\x1b[?2004h")
	run.pty.send(t, pasteOf(message(body)))
	run.pty.send(t, "\r")
	waitUntil(t, "the commit", func() bool { return r.committed(t) })
	run.pty.waitFor(t, 0, "committed canary/one")
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("text of the message ran")
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "canary-token.txt")); string(b) != testToken+"\n" {
		t.Fatalf("file %q", b)
	}
	// Nothing of the message but the token is in the commit.
	if got := runGit(t, r.Dir, "show", "--format=%B", "HEAD"); !strings.HasPrefix(got, "canary: token") || strings.Contains(got, "touch") {
		t.Fatalf("commit text %q", got)
	}
	if got := runGit(t, r.Dir, "show", "--name-only", "--format=", "HEAD"); got != "canary-token.txt" {
		t.Fatalf("the commit holds %q", got)
	}
}

// What the stand-in shows of a paste has its control characters made visible,
// so a message cannot drive the terminal through the echo.
func TestPlainEchoShowsNoControlSequence(t *testing.T) {
	r := newRepo(t)
	run := startRun(t, r, 0)
	i := run.pty.waitFor(t, 0, "\x1b[?2004h")
	run.pty.send(t, pasteOf("a\x1b[31mred\x07"))
	end := run.pty.waitFor(t, i, "^G")
	shown := run.pty.output()[i:end]
	if bytes.Contains(shown, []byte("\x1b")) || bytes.Contains(shown, []byte("\x07")) {
		t.Fatalf("the echo holds a control character: %q", shown)
	}
	if !bytes.Contains(shown, []byte("a^[[31mred^G")) {
		t.Fatalf("the echo is %q", shown)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestPlainTerminalIsRawAndRestored(t *testing.T) {
	r := newRepo(t)
	p := openPTY(t)
	before, err := unix.IoctlGetTermios(int(p.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lflag&unix.ICANON == 0 || before.Lflag&unix.ECHO == 0 {
		t.Fatalf("a new terminal is not cooked: %#x", before.Lflag)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Dir: r.Dir, Home: r.Home, In: p.slave, Out: p.slave, TermFD: int(p.slave.Fd())})
	}()
	p.waitFor(t, 0, "\x1b[?2004h")
	raw, err := unix.IoctlGetTermios(int(p.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 || raw.Oflag&unix.OPOST != 0 || raw.Iflag&unix.ICRNL != 0 {
		t.Fatalf("the terminal is not raw: %+v", raw)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a done context", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	after, err := unix.IoctlGetTermios(int(p.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatalf("the terminal was not restored:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestPlainReturnsNilAtEndOfInput(t *testing.T) {
	r := newRepo(t)
	var out bytes.Buffer
	err := Run(context.Background(), Config{Dir: r.Dir, Home: r.Home, In: strings.NewReader("abc"), Out: &out, TermFD: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> ") || !strings.Contains(out.String(), "abc") {
		t.Fatalf("output %q", out.String())
	}
}

func TestRunNeedsATerminalWhenAskedForRawMode(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), Config{In: f, Out: &out, TermFD: int(f.Fd())}); err == nil {
		t.Fatal("Run took /dev/null for a terminal")
	}
	if out.Len() != 0 {
		t.Fatalf("Run drew %q before it had a terminal", out.String())
	}
}
