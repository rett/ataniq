package noaicheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	f := newFixture(t)
	f.file("usr/bin/x", "")
	f.link("bin", "usr/bin")
	f.link("abs", "/usr/bin")
	f.link("up", "../../../../usr")
	f.link("chain", "abs")
	f.link("loop/a", "b")
	f.link("loop/b", "a")
	f.link("self", "self")
	f.link("dangling", "/missing/place")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	cases := []struct {
		in, want string
		err      error
	}{
		{"usr/bin/x", "usr/bin/x", nil},
		{"/usr/bin/x", "usr/bin/x", nil},
		{"bin/x", "usr/bin/x", nil},
		{"abs/x", "usr/bin/x", nil},
		{"up/bin", "usr/bin", nil},
		{"chain/x", "usr/bin/x", nil},
		{"../../bin", "usr/bin", nil},
		{".", ".", nil},
		{"loop/a", "", errLoop},
		{"self/x", "", errLoop},
		{"dangling", "", fs.ErrNotExist},
		{"usr/bin/x/y", "", nil},
	}
	for _, tc := range cases {
		var got string
		var err error
		within(t, func() { got, err = tr.resolve(tc.in) })
		switch {
		case tc.in == "usr/bin/x/y":
			if err == nil {
				t.Errorf("%s: resolved below a file to %q", tc.in, got)
			}
		case tc.err != nil:
			if !errors.Is(err, tc.err) {
				t.Errorf("%s: err = %v, want %v", tc.in, err, tc.err)
			}
		case err != nil || got != tc.want:
			t.Errorf("%s: got %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestReadFileRefusesNonRegular(t *testing.T) {
	f := newFixture(t)
	f.dir("etc/profile.d")
	f.file("etc/big", string(make([]byte, 100)))
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	if _, _, err := tr.readFile("etc/profile.d", 10); err == nil {
		t.Error("read a folder as a file")
	}
	if _, _, err := tr.readFile("etc/big", 10); !errors.Is(err, errTooLarge) {
		t.Errorf("read past the limit: %v", err)
	}
	if _, data, err := tr.readFile("etc/big", 100); err != nil || len(data) != 100 {
		t.Errorf("read at the limit: %d bytes, %v", len(data), err)
	}
}

// A walk does not follow links, ends on a loop and reports an unreadable
// folder instead of stopping.
func TestWalk(t *testing.T) {
	skipIfRoot(t)
	f := newFixture(t)
	f.file("opt/a/b/c/d/e", "")
	f.link("opt/loop", ".")
	f.link("opt/a/up", "../..")
	f.unreadable("opt/locked")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	var seen []string
	var failed []string
	var deep bool
	within(t, func() {
		_, deep, _ = tr.walk("opt", 4, func(e *wentry) bool { seen = append(seen, e.rel); return true },
			func(rel string, err error) { failed = append(failed, rel) })
	})
	if !deep {
		t.Error("a walk stopped by its depth did not say so")
	}
	want := map[string]bool{"opt/a/b/c/d": true, "opt/loop": true, "opt/a/up": true, "opt/locked": true}
	for _, s := range seen {
		if s == "opt/a/b/c/d/e" {
			t.Error("walked deeper than 4 levels")
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Errorf("not visited: %v (visited %v)", want, seen)
	}
	if len(failed) != 1 || failed[0] != "opt/locked" {
		t.Errorf("failed = %v, want opt/locked", failed)
	}
}

func TestField(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/claude":    "/usr/bin/claude",
		"":                   `""`,
		"two words":          `"two words"`,
		"x\nok":              `"x\nok"`,
		"a\"b":               `"a\"b"`,
		"\xff":               `"\xff"`,
		"caf\u00e9":          `"caf\u00e9"`,
		"\u2028":             `"\u2028"`,
		"esc\x1b[2J":         `"esc\x1b[2J"`,
		"c1\u009b":           `"c1\u009b"`,
		"back\\slash":        `"back\\slash"`,
		"bidi\u202eexe":      `"bidi\u202eexe"`,
		"/home/user/.claude": "/home/user/.claude",
	}
	for in, want := range cases {
		if got := field(in); got != want {
			t.Errorf("field(%q) = %s, want %s", in, got, want)
		}
	}
}

// Field is field, bounded: a longer string is cut to MaxField bytes and
// "..." follows it, outside any quotes.
func TestFieldBounded(t *testing.T) {
	long := strings.Repeat("a", MaxField+10)
	for in, want := range map[string]string{
		"/usr/bin/claude": "/usr/bin/claude",
		"x\nok":           `"x\nok"`,
		long:              long[:MaxField] + "...",
		"\x1b" + long:     strconv.QuoteToASCII("\x1b"+long[:MaxField-1]) + "...",
	} {
		if got := Field(in); got != want {
			t.Errorf("Field(%.20q) = %.40s", in, got)
		}
	}
}

// A file swapped between the type check and the open is refused, not read:
// a FIFO by the descriptor's type, another file by its identity.
func TestReadFileChecksTheOpenedFile(t *testing.T) {
	f := newFixture(t)
	f.file("etc/environment", "LANG=C\n")
	f.file("etc/other", "OTHER\n")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	defer func() { testHookBeforeOpen = nil }()

	testHookBeforeOpen = func(string) { mkfifo(f, "etc/environment") }
	within(t, func() { _, _, err = tr.readFile("etc/environment", 100) })
	if !errors.Is(err, errNotRegular) {
		t.Errorf("a FIFO swapped in: %v", err)
	}
	mustRemoveAll(f, "etc/environment")
	f.file("etc/environment", "LANG=C\n")
	testHookBeforeOpen = func(string) {
		if err := os.Rename(f.abs("etc/other"), f.abs("etc/environment")); err != nil {
			t.Fatal(err)
		}
	}
	if _, data, err := tr.readFile("etc/environment", 100); !errors.Is(err, errChanged) || data != nil {
		t.Errorf("a file swapped in: %q, %v", data, err)
	}
}

// A folder replaced by a link after it was listed is not entered: the walk
// opens it relative to its parent without following a link.
func TestWalkDoesNotFollowASwappedFolder(t *testing.T) {
	f := newFixture(t)
	f.file("opt/a/inner", "")
	f.file("etc/secret-place/x", "")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	var seen, failed []string
	tr.walk("opt", 3, func(e *wentry) bool {
		seen = append(seen, e.rel)
		if e.rel == "opt/a" {
			mustRemoveAll(f, "opt/a")
			f.link("opt/a", "/etc/secret-place")
		}
		return true
	}, func(rel string, err error) { failed = append(failed, rel) })
	if !reflect.DeepEqual(seen, []string{"opt/a"}) || !reflect.DeepEqual(failed, []string{"opt/a"}) {
		t.Errorf("seen %v failed %v", seen, failed)
	}
}

// A folder wider than one batch is listed whole, in name order within each
// batch, and the memory it holds is given back.
func TestWalkStreamsWideFolders(t *testing.T) {
	f := newFixture(t)
	n := dirBatch*2 + 17
	for i := 0; i < n; i++ {
		f.file(fmt.Sprintf("opt/wide/f%05d", i), "")
	}
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	var seen []string
	tr.walk("opt/wide", 1, func(e *wentry) bool { seen = append(seen, e.name); return false }, func(string, error) {})
	if len(seen) != n {
		t.Fatalf("saw %d of %d entries", len(seen), n)
	}
	for i := 0; i < n; i += dirBatch {
		end := min(i+dirBatch, n)
		if !slices.IsSorted(seen[i:end]) {
			t.Errorf("batch at %d is not in order", i)
		}
	}
	if tr.b.memory != 0 {
		t.Errorf("%d bytes still held after the walk", tr.b.memory)
	}
}

// Every limit of the budget ends the run with "incomplete budget <limit>",
// never a pass; the walk stops instead of listing on.
func TestBudget(t *testing.T) {
	cases := []struct {
		name string
		lim  limits
		want string
	}{
		{"entries", limits{entries: 10, bytes: 1 << 30, time: time.Hour, memory: 1 << 30}, budgetEntries},
		{"bytes", limits{entries: 1 << 30, bytes: 64, time: time.Hour, memory: 1 << 30}, budgetBytes},
		{"time", limits{entries: 1 << 30, bytes: 1 << 30, time: -time.Second, memory: 1 << 30}, budgetTime},
		{"memory", limits{entries: 1 << 30, bytes: 1 << 30, time: time.Hour, memory: direntBuf + 512}, budgetMemory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			for i := 0; i < 50; i++ {
				f.file(fmt.Sprintf("opt/many/entry-with-a-long-name-%03d", i), "")
			}
			c := f.checker(testLists())
			c.t.b = newBudget(tc.lim)
			var r Report
			within(t, func() { r = c.RunAll(nil) })
			last := r.Hits[len(r.Hits)-1]
			if last != (Hit{Kind: KindIncomplete, Path: PathBudget, Detail: tc.want}) || r.Complete {
				t.Errorf("hits %v", r.Hits)
			}
			for _, h := range r.Hits {
				if h.Kind == KindUnreadable && strings.Contains(h.Detail, "budget") {
					t.Errorf("the budget reported as unreadable: %v", h)
				}
			}
		})
	}
}

// A walk stopped by its depth limit is a hit, never a silent skip.
func TestWalkDepthIsReported(t *testing.T) {
	f := newFixture(t)
	f.file("opt/a/b/c", "")
	c := f.checker(testLists())
	c.walk("opt", 1, func(*wentry) bool { return true })
	if want := []Hit{{Kind: KindIncomplete, Path: "/opt", Detail: DetailDepth}}; !reflect.DeepEqual(c.Report().Hits, want) {
		t.Errorf("hits %v", c.Report().Hits)
	}
}

// statAt reads the type and mode without opening a FIFO or following a link.
func TestStatAt(t *testing.T) {
	f := newFixture(t)
	f.file("d/exe", "")
	if err := os.Chmod(f.abs("d/exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.link("d/link", "exe")
	mkfifo(f, "d/fifo")
	fd, err := syscall.Open(f.abs("d"), syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	for name, want := range map[string]fs.FileMode{"exe": 0o755, "link": fs.ModeSymlink, "fifo": fs.ModeNamedPipe} {
		var m fs.FileMode
		within(t, func() { m, err = statAt(fd, name) })
		if err != nil || (name == "exe" && m != want) || (name != "exe" && m.Type() != want) {
			t.Errorf("%s: %v %v", name, m, err)
		}
	}
}

// An entry whose type the kernel did not give and a stat cannot find is
// reported to the walk's fail as errUntyped, and the check makes it
// "incomplete <path> type": the walk cannot know whether to enter it.
func TestWalkReportsAnUnresolvedType(t *testing.T) {
	f := newFixture(t)
	f.dir("opt/a/inner")
	f.file("opt/b", "")
	defer func() { testHookDirent = nil }()
	testHookDirent = func(name string) (byte, error) {
		if name == "a" {
			return syscall.DT_UNKNOWN, syscall.EIO
		}
		return 0, nil
	}
	c := f.checker(testLists())
	c.walk("opt", 4, func(*wentry) bool { return true })
	want := []Hit{{Kind: KindIncomplete, Path: "/opt/a", Detail: DetailType}}
	if r := c.Report(); !reflect.DeepEqual(r.Hits, want) || r.Complete {
		t.Errorf("hits %v complete %v", r.Hits, r.Complete)
	}
}

// Every hit that keeps the check from covering a place makes the run
// incomplete; a finding alone does not.
func TestCoverageHitsClearComplete(t *testing.T) {
	for kind, complete := range map[string]bool{
		KindUnreadable: false, KindUnitLink: false, KindIncomplete: false, KindPolicy: false,
		"some-new-kind": false,
		KindBinary:      true, KindTool: true, KindCredential: true, KindEnv: true, KindProcess: true,
		KindUnit: true, KindPort: true, KindPackage: true, KindNFT: true,
	} {
		c := newFixture(t).checker(testLists())
		c.hit(kind, "/x", "")
		if got := c.Report().Complete; got != complete {
			t.Errorf("a %s hit: complete %v, want %v", kind, got, complete)
		}
	}
}
