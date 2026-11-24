//go:build linux && ataniq_canary

package standin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var (
	replyNameRE   = regexp.MustCompile(`^[0-9]{4}-standin\.txt$`)
	replyTextRE   = regexp.MustCompile(`^Stand-in: handled [0-9a-f]{7}\.$`)
	fortyHexRunRE = regexp.MustCompile(`[0-9a-f]{40}`)
)

func outboxPath(r testRepo) string { return filepath.Join(r.Home, OutboxName) }

func makeOutbox(t testing.TB, r testRepo) string {
	t.Helper()
	dir := outboxPath(r)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func listNames(t testing.TB, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// pasteAndWaitForCommit runs the stand-in on a pty, pastes a token message, submits it
// and waits for the commit.
func pasteAndWaitForCommit(t *testing.T, r testRepo, run *standinRun, token string) {
	t.Helper()
	before := r.head(t)
	run.pty.send(t, pasteOf(TokenPrefix+token)+"\r")
	waitUntil(t, "the commit", func() bool { return r.head(t) != before })
}

func TestReplyFolderMissingWritesNothing(t *testing.T) {
	r := newRepo(t)
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(outboxPath(r)); !os.IsNotExist(err) {
		t.Fatalf("the reply folder exists after the run: %v", err)
	}
	if got := listNames(t, r.Home); len(got) != 0 {
		t.Fatalf("the home holds %v, want nothing", got)
	}
}

func TestReplyOneFileForOneCommit(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	names := listNames(t, dir)
	if len(names) != 1 || !replyNameRE.MatchString(names[0]) {
		t.Fatalf("the folder holds %v, want one <seq>-standin.txt", names)
	}
	b, err := os.ReadFile(filepath.Join(dir, names[0]))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !replyTextRE.MatchString(text) || len(b) > MaxReplyBytes {
		t.Fatalf("reply %q", text)
	}
	if want := "Stand-in: handled " + r.head(t)[:7] + "."; text != want {
		t.Fatalf("reply %q, want %q", text, want)
	}
	if fortyHexRunRE.MatchString(text) {
		t.Fatalf("reply %q holds a 40-digit hexadecimal run", text)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the folder's mode changed: %v, %v", st, err)
	}
}

func TestReplyOnePerCommitAndNoNameReused(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	pasteAndWaitForCommit(t, r, run, "second-"+testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	names := listNames(t, dir)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("the folder holds %v, want two distinct replies", names)
	}
}

func TestReplyStopsAtThreeWaiting(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	for _, n := range []string{"0001-standin.txt", "0002-standin.txt", "0003-standin.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("waiting"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A file that is not the stand-in's is not counted.
	if err := os.WriteFile(filepath.Join(dir, "0009-other.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, dir); len(names) != 4 {
		t.Fatalf("the folder holds %v, want the four files it had", names)
	}
}

func TestReplyCountsOnlyItsOwnFiles(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	for _, n := range []string{"0001-standin.txt", "0002-standin.txt", "a-other.txt", "b-other.txt", ".draft"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, dir); len(names) != 6 {
		t.Fatalf("the folder holds %v, want one new file", names)
	}
}

func TestReplyNotWritableWritesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to any folder")
	}
	r := newRepo(t)
	dir := makeOutbox(t, r)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, dir); len(names) != 0 {
		t.Fatalf("the folder holds %v, want nothing", names)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o500 {
		t.Fatalf("the folder's mode changed: %v, %v", st, err)
	}
}

func TestReplyFolderThatIsALinkIsNotFollowed(t *testing.T) {
	r := newRepo(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, outboxPath(r)); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, r, 100*time.Millisecond)
	pasteAndWaitForCommit(t, r, run, testToken)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, target); len(names) != 0 {
		t.Fatalf("the link's target holds %v, want nothing", names)
	}
}

func TestNoReplyWithoutACommit(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	run := startRun(t, r, 100*time.Millisecond)
	// The stand-in draws its prompt once the terminal is raw. Until then the
	// terminal's own echo answers a paste, and a terminal closed before the
	// stand-in has set it up makes Run fail with EIO.
	i := run.pty.waitFor(t, 0, "\x1b[?2004h")
	run.pty.send(t, pasteOf("no token here")+"\r")
	// The stand-in's own answer to the submitted paste: it has read all of it.
	run.pty.waitFor(t, i, "\r\nno token\r\n> ")
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, dir); len(names) != 0 {
		t.Fatalf("the folder holds %v, want nothing", names)
	}
}

func TestWriteReplyRefusesAnIDThatIsNotShortHex(t *testing.T) {
	r := newRepo(t)
	dir := makeOutbox(t, r)
	for _, id := range []string{"", "ABCDEF1", "abcdef12", "../../x", "abc def"} {
		if _, err := writeReply(r.Home, 1, id); err == nil {
			t.Errorf("writeReply accepted %q", id)
		}
	}
	if names := listNames(t, dir); len(names) != 0 {
		t.Fatalf("the folder holds %v", names)
	}
}

// The folder is held from the listing to the rename: a folder that replaces
// it meanwhile (a turn-on makes a new one) gets nothing, and the reply ends
// in the folder that was listed.
func TestReplyHoldsTheFolderItListed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, OutboxName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(home, "moved")
	replyHook = func() {
		if err := os.Rename(dir, moved); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Error(err)
		}
	}
	defer func() { replyHook = nil }()
	if _, err := writeReply(home, 1, "abc1234"); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("the new folder holds %v", ents)
	}
	if b, err := os.ReadFile(filepath.Join(moved, "0001"+ReplySuffix)); err != nil || string(b) != replyText("abc1234") {
		t.Fatalf("the listed folder's reply: %q, %v", b, err)
	}
}

// A folder that holds more names than the stand-in reads gets no reply.
func TestReplySkipsAFolderPastTheBound(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, OutboxName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxListed; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".draft-%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeReply(home, 1, "abc1234"); err == nil {
		t.Fatal("a reply was written past the bound")
	}
	if _, err := os.Lstat(filepath.Join(dir, "0001"+ReplySuffix)); !os.IsNotExist(err) {
		t.Fatalf("a reply is there: %v", err)
	}
}
