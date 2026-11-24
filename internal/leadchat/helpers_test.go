//go:build linux && ataniq_canary

package leadchat

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// testRole is a scratch lead's layout role; every fixture is synthetic.
const testRole = "scratch-alpha-lead"

// clock is the real clock plus an offset the test moves. The settle and
// stale rules weigh the kernel's change times against it, so a test that
// moves it a few seconds ahead sees a file it just wrote as settled.
type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) set(d time.Duration) {
	c.mu.Lock()
	c.offset = d
	c.mu.Unlock()
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

// env is a state root with one lead's home and outbox.
type env struct {
	t      *testing.T
	dir    string
	root   *Root
	outbox string
	clock  *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, clock: &clock{offset: 5 * time.Second}}
	e.outbox = e.makeOutbox(testRole)
	root, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e.root = root
	return e
}

// makeOutbox makes the folders of a lead's outbox, mode 0700 each.
func (e *env) makeOutbox(role string) string {
	e.t.Helper()
	p := e.dir
	for _, name := range outboxPath(role) {
		p = filepath.Join(p, name)
		if err := os.Mkdir(p, 0o700); err != nil && !os.IsExist(err) {
			e.t.Fatal(err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			e.t.Fatal(err)
		}
	}
	return p
}

// open opens the lead's store with the env's clock and closes it at the end.
func (e *env) open() *Store {
	e.t.Helper()
	s, err := e.openRole(testRole)
	if err != nil {
		e.t.Fatalf("OpenStore: %v", err)
	}
	return s
}

func (e *env) openRole(role string) (*Store, error) {
	s, err := OpenStore(e.root, role, Options{Now: e.clock.now})
	if err == nil {
		e.t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

// on opens the store and turns chat on.
func (e *env) on() *Store {
	e.t.Helper()
	s := e.open()
	if _, err := s.SetSwitch("read", 1); err != nil {
		e.t.Fatalf("SetSwitch read: %v", err)
	}
	return s
}

// reply writes a reply the way the instructions ask: a dot-name, then a
// rename.
func (e *env) reply(name, text string) {
	e.t.Helper()
	tmp := filepath.Join(e.outbox, ".writing")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(e.outbox, name)); err != nil {
		e.t.Fatal(err)
	}
}

// names lists the outbox, sorted.
func (e *env) names() []string {
	e.t.Helper()
	return e.list(e.outbox)
}

// file writes a file of the lead in the outbox under name, as it is.
func (e *env) file(name, text string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.outbox, name), []byte(text), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// claimDir is the lead's claim folder.
func (e *env) claimDir() string { return filepath.Join(e.dir, ChatDirName, claimsName(testRole)) }

// claims lists the lead's claim folder, sorted.
func (e *env) claims() []string {
	e.t.Helper()
	return e.list(e.claimDir())
}

// quarDir is the lead's quarantine.
func (e *env) quarDir() string { return filepath.Join(e.dir, "leads", testRole, QuarantineName) }

// quarantined lists the lead's quarantine, sorted; none is an empty list.
func (e *env) quarantined() []string {
	e.t.Helper()
	if _, err := os.Lstat(e.quarDir()); os.IsNotExist(err) {
		return nil
	}
	return e.list(e.quarDir())
}

// list lists a folder, sorted.
func (e *env) list(dir string) []string {
	e.t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, d := range ents {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out
}

func (e *env) chatFile(role string) string { return filepath.Join(e.dir, ChatDirName, role+".jsonl") }

// lines reads the chat file's lines.
func (e *env) lines(role string) []string {
	e.t.Helper()
	f, err := os.Open(e.chatFile(role))
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, MaxLineBytes*2), MaxLineBytes*2)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func countKind(lines []string, kind string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, `"kind":"`+kind+`"`) {
			n++
		}
	}
	return n
}

func ingest(t *testing.T, s *Store) IngestReport {
	t.Helper()
	rep, err := s.Ingest(1)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return rep
}

func read(t *testing.T, s *Store) []Entry {
	t.Helper()
	es, _, _, err := s.Read(0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return es
}

func texts(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func wantTexts(t *testing.T, s *Store, want ...string) {
	t.Helper()
	if got := texts(read(t, s)); !slices.Equal(got, want) {
		t.Fatalf("entries\n got %q\nwant %q", got, want)
	}
}

func seqs(es []Entry) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}
