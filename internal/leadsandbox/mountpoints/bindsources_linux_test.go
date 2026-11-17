//go:build linux

package mountpoints

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

func TestCheckBindSourcesCleanPaths(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.write(f.base+"/file", "x")
	rep := CheckBindSources([]string{"/", f.base, f.base + "/file", f.home, f.in.AgentInstall, f.in.ProbeBinary})
	if !rep.OK() {
		t.Fatalf("%v", rep.Err())
	}
	if rep := CheckBindSources(nil); !rep.OK() {
		t.Fatalf("no sources: %v", rep.Err())
	}
}

func TestCheckBindSourcesRefusesALinkAnywhereAndNamesTheSource(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.out+"/real/inner", 0o755)
	f.write(f.out+"/real/inner/file", "x")
	f.write(f.out+"/real/file", "x")
	tests := []struct {
		name   string
		plant  func()
		source string
		link   string // the component the problem names
	}{
		{"the last component", func() { f.symlink(f.out+"/real/file", f.base+"/l-last") }, f.base + "/l-last", f.base + "/l-last"},
		{"the last component, a folder", func() { f.symlink(f.out+"/real", f.base+"/l-dir") }, f.base + "/l-dir", f.base + "/l-dir"},
		{"a middle component", func() { f.symlink(f.out+"/real", f.base+"/l-mid") }, f.base + "/l-mid/inner/file", f.base + "/l-mid"},
		{"a middle component that points back inside", func() { f.symlink(f.out+"/real/inner", f.out+"/l-back") }, f.out + "/l-back/file", f.out + "/l-back"},
		{"a dangling link", func() { f.symlink(f.base+"/nowhere", f.base+"/l-dangling") }, f.base + "/l-dangling", f.base + "/l-dangling"},
		{"a relative link", func() { f.symlink("outside/real", f.base+"/l-rel") }, f.base + "/l-rel/file", f.base + "/l-rel"},
		{"a link to a link", func() {
			f.symlink(f.out+"/real", f.base+"/l-one")
			f.symlink(f.base+"/l-one", f.base+"/l-two")
		}, f.base + "/l-two/file", f.base + "/l-two"},
		{"a link below a lead folder", func() { f.symlink(f.out+"/real", f.tree+"/l-tree") }, f.tree + "/l-tree/inner", f.tree + "/l-tree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.plant()
			// What a path-based open would do: it follows the link.
			if _, err := os.Stat(tt.source); err != nil && tt.name != "a dangling link" {
				t.Fatalf("the planted path does not resolve: %v", err)
			}
			rep := CheckBindSources([]string{f.base + "/file-before-ok", tt.source})
			var found *Problem
			for _, p := range rep.Problems {
				if p.Path == tt.source {
					found = p
				}
			}
			if found == nil || !errors.Is(found, ErrSymlink) {
				t.Fatalf("the source %s was not refused as a link: %v", tt.source, rep.Err())
			}
			if !strings.Contains(found.Detail, tt.link) {
				t.Errorf("the problem does not name the link %s: %q", tt.link, found.Detail)
			}
			// The missing neighbour is a separate problem about its own path.
			wantProblem(t, rep, ErrMissing, f.base+"/file-before-ok")
		})
	}
}

func TestCheckBindSourcesDanglingLinkIsALinkNotMissing(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.symlink(f.base+"/nowhere", f.base+"/dangling")
	rep := CheckBindSources([]string{f.base + "/dangling"})
	wantProblem(t, rep, ErrSymlink, f.base+"/dangling")
	if rep.Has(ErrMissing) {
		t.Fatalf("a link is not a missing entry: %v", rep.Err())
	}
}

func TestCheckBindSourcesMagicLink(t *testing.T) {
	if _, err := os.Lstat("/proc/self/exe"); err != nil {
		t.Skip("no /proc")
	}
	for _, src := range []string{"/proc/self/exe", "/proc/self/cwd", "/proc/self/fd/0"} {
		rep := CheckBindSources([]string{src})
		if !rep.Has(ErrSymlink) {
			t.Errorf("%s: %v", src, rep.Err())
		}
	}
}

func TestCheckBindSourcesOtherRefusals(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.write(f.base+"/file", "x")
	tests := []struct {
		name string
		src  string
		kind error
	}{
		{"missing", f.base + "/absent", ErrMissing},
		{"missing parent", f.base + "/absent/inner", ErrMissing},
		{"below a file", f.base + "/file/inner", ErrWrongType},
		{"relative", "state/home", ErrInvalid},
		{"unclean", f.base + "/./file", ErrInvalid},
		{"dot dot", f.base + "/state/../file", ErrInvalid},
		{"trailing slash", f.base + "/", ErrInvalid},
		{"empty", "", ErrInvalid},
		{"control character", f.base + "/a\nb", ErrInvalid},
		{"NUL", f.base + "/a\x00b", ErrInvalid},
		{"too long", "/" + strings.Repeat("a/", 3000), ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := CheckBindSources([]string{tt.src})
			if !rep.Has(tt.kind) || len(rep.Problems) != 1 {
				t.Fatalf("want one %v problem, got %v", tt.kind, rep.Err())
			}
			if p := rep.Problems[0]; len(p.Path) > maxDetail+3 || len(p.Detail) > maxDetail+3 {
				t.Errorf("a problem is not bounded: %d, %d", len(p.Path), len(p.Detail))
			}
		})
	}
}

func TestCheckBindSourcesReportsEverySourceInOrderAndIsBounded(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.symlink(f.out, f.base+"/l")
	rep := CheckBindSources([]string{f.base + "/absent", f.base + "/l", f.home, f.base + "/l/x"})
	if len(rep.Problems) != 3 || rep.Problems[0].Path != f.base+"/absent" || rep.Problems[1].Path != f.base+"/l" || rep.Problems[2].Path != f.base+"/l/x" {
		t.Fatalf("got %v", rep.Err())
	}
	many := make([]string, MaxProblems+20)
	for i := range many {
		many[i] = f.base + "/absent"
	}
	rep = CheckBindSources(many)
	if len(rep.Problems) != MaxProblems || !rep.Truncated {
		t.Fatalf("%d problems, truncated %v", len(rep.Problems), rep.Truncated)
	}
}

func TestCheckBindSourcesWithoutOpenat2(t *testing.T) {
	f := newFixture(t, profile.Claude)
	for _, errno := range []error{unix.ENOSYS, unix.EPERM} {
		rep := checkBindSources([]string{f.home}, func(int, string, *unix.OpenHow) (int, error) { return -1, errno })
		wantProblem(t, rep, ErrUnreadable, f.home)
		if !errors.Is(rep.Problems[0], errno) {
			t.Errorf("the errno %v is not kept: %v", errno, rep.Err())
		}
	}
	// An interrupted call is retried; the real call is used for the rest.
	calls := 0
	rep := checkBindSources([]string{f.home}, func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		if calls++; calls < 3 {
			return -1, unix.EINTR
		}
		return unix.Openat2(dirfd, p, how)
	})
	if !rep.OK() || calls != 3 {
		t.Fatalf("%v after %d calls", rep.Err(), calls)
	}
}

func TestCheckBindSourcesChecksTheRealPathNotTheLexicalOne(t *testing.T) {
	// A source whose lexical path goes through a link is refused as it is;
	// the real path ResolveSources gives passes.
	f := newFixture(t, profile.Claude)
	real := f.base + "/real-agent"
	f.mkdir(real, 0o755)
	f.remove(f.in.AgentInstall)
	f.symlink(real, f.in.AgentInstall)
	res := f.resolve()
	if !res.OK() {
		t.Fatalf("%v", res.Err())
	}
	var lexical, realPaths []string
	for _, s := range res.Sources {
		if s.Skipped || !strings.HasPrefix(s.Real, f.base+"/") {
			continue
		}
		lexical = append(lexical, s.Source)
		realPaths = append(realPaths, s.Real)
	}
	wantProblem(t, CheckBindSources(lexical), ErrSymlink, f.in.AgentInstall)
	if rep := CheckBindSources(realPaths); !rep.OK() {
		t.Fatalf("the real paths: %v", rep.Err())
	}
	if filepath.Base(realPaths[0]) == "" {
		t.Fatal("no paths")
	}
}
