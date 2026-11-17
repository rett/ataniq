//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// passFixture makes the files the pass-through tests open.
type passFixture struct {
	dir, a, b, fifo, link, sub string
}

func newPassFixture(t *testing.T) passFixture {
	t.Helper()
	dir := t.TempDir()
	f := passFixture{dir: dir, a: filepath.Join(dir, "a.txt"), b: filepath.Join(dir, "b.txt"),
		fifo: filepath.Join(dir, "f.fifo"), link: filepath.Join(dir, "link.txt"), sub: filepath.Join(dir, "sub")}
	for p, data := range map[string]string{f.a: "alpha", f.b: "beta"} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(f.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.a, f.link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.sub, 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func startPass(t *testing.T, o runOpts, opts ...string) *probeRun {
	t.Helper()
	out := filepath.Join(t.TempDir(), "agent.json")
	args := append([]string{"pass-fds"}, opts...)
	args = append(args, "--", testExe(t), "agent", out, "0", "0")
	return start(t, o, args, out, false)
}

func fdByNumber(a agentReport, n int) (agentFD, bool) {
	for _, f := range a.FDs {
		if f.FD == n {
			return f, true
		}
	}
	return agentFD{}, false
}

func TestPassFDsPlacesFilesAtTheirNumbers(t *testing.T) {
	f := newPassFixture(t)
	for _, c := range []struct {
		name string
		opts []string
		// number -> want read-only (else write-only append) and the file
		want map[int]struct {
			write bool
			path  string
		}
		leak int
	}{
		{"a swap of the numbers the opens land on", []string{"--read", "4=" + f.a, "--write", "3=" + f.b},
			map[int]struct {
				write bool
				path  string
			}{4: {false, f.a}, 3: {true, f.b}}, 3},
		{"already in place", []string{"--read", "3=" + f.a, "--read", "4=" + f.a},
			map[int]struct {
				write bool
				path  string
			}{3: {false, f.a}, 4: {false, f.a}}, 2},
		{"a chain", []string{"--read", "5=" + f.a, "--read", "3=" + f.a, "--write", "4=" + f.b},
			map[int]struct {
				write bool
				path  string
			}{3: {false, f.a}, 4: {true, f.b}, 5: {false, f.a}}, 0},
		{"high numbers", []string{"--read", "700=" + f.a, "--write", "1023=" + f.b},
			map[int]struct {
				write bool
				path  string
			}{700: {false, f.a}, 1023: {true, f.b}}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			beforeB, _ := os.ReadFile(f.b)
			r := startPass(t, runOpts{mode: modeFake, leak: c.leak}, c.opts...)
			if code := r.wait(); code != 0 {
				t.Fatalf("exit %d: %s", code, r.stderr)
			}
			a := r.agent()
			if len(a.FDs) != len(c.want) {
				t.Errorf("descriptors %+v, want only %v", a.FDs, c.want)
			}
			for n, w := range c.want {
				got, ok := fdByNumber(a, n)
				if !ok {
					t.Errorf("descriptor %d is missing", n)
					continue
				}
				if got.Cloexec {
					t.Errorf("descriptor %d is close-on-exec", n)
				}
				if got.Link != w.path {
					t.Errorf("descriptor %d is %q, want %q", n, got.Link, w.path)
				}
				acc := got.Flags & unix.O_ACCMODE
				switch {
				case w.write && (acc != unix.O_WRONLY || got.Flags&unix.O_APPEND == 0):
					t.Errorf("descriptor %d flags %#o, want write-only and append", n, got.Flags)
				case !w.write && (acc != unix.O_RDONLY || got.Flags&unix.O_NONBLOCK != 0 || got.Head != "alpha"):
					t.Errorf("descriptor %d flags %#o head %q, want read-only, blocking, alpha", n, got.Flags, got.Head)
				}
			}
			if !slices.Equal(sorted(a.Env), sorted(r.env)) {
				t.Errorf("environment %q, want %q", a.Env, r.env)
			}
			// Appended to, never truncated or created anew.
			gotB, _ := os.ReadFile(f.b)
			if !strings.HasPrefix(string(gotB), string(beforeB)) {
				t.Errorf("b.txt was %q, now %q", beforeB, gotB)
			}
			os.WriteFile(f.b, []byte("beta"), 0o600)
		})
	}
}

func TestPassFDsFIFOWithAReader(t *testing.T) {
	f := newPassFixture(t)
	// The holder opens its end first.
	rd, err := unix.Open(f.fifo, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rd)
	r := startPass(t, runOpts{mode: modeFake}, "--write", "9="+f.fifo)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.stderr)
	}
	a := r.agent()
	if got, ok := fdByNumber(a, 9); !ok || got.Link != f.fifo || got.Flags&unix.O_ACCMODE != unix.O_WRONLY {
		t.Errorf("descriptor 9: %+v", a.FDs)
	}
	buf := make([]byte, 8)
	if n, _ := unix.Read(rd, buf); string(buf[:n]) != "hi" {
		t.Errorf("the FIFO got %q", buf[:n])
	}
}

func TestPassFDsNoFilesStillSweeps(t *testing.T) {
	r := startPass(t, runOpts{mode: modeFake, leak: 3})
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d: %s", code, r.stderr)
	}
	if a := r.agent(); len(a.FDs) != 0 {
		t.Errorf("descriptors %+v", a.FDs)
	}
}

func TestPassFDsRefusals(t *testing.T) {
	f := newPassFixture(t)
	for _, c := range []struct {
		name string
		opts []string
		want string
	}{
		{"read a link", []string{"--read", "4=" + f.link}, "symbolic link"},
		{"read a folder", []string{"--read", "4=" + f.sub}, "not a regular file"},
		{"read a FIFO", []string{"--read", "4=" + f.fifo}, "not a regular file"},
		{"read a device", []string{"--read", "4=/dev/null"}, "not a regular file"},
		{"read what is missing", []string{"--read", "4=" + filepath.Join(f.dir, "none")}, "no such file"},
		{"write a link", []string{"--write", "4=" + f.link}, "neither a regular file nor a FIFO"},
		{"write a folder", []string{"--write", "4=" + f.sub}, "neither a regular file nor a FIFO"},
		{"write a device", []string{"--write", "4=/dev/null"}, "neither a regular file nor a FIFO"},
		{"write what is missing", []string{"--write", "4=" + filepath.Join(f.dir, "none")}, "no such file"},
		{"the second file is refused", []string{"--read", "4=" + f.a, "--read", "5=" + f.sub}, "not a regular file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := startPass(t, runOpts{mode: modeFake}, c.opts...)
			if code := r.wait(); code != exitSetup {
				t.Errorf("exit %d, want %d", code, exitSetup)
			}
			oneLine(t, r.stderr.String())
			if !strings.HasPrefix(r.stderr.String(), "ataniq-probe: pass-fds: ") || !strings.Contains(r.stderr.String(), c.want) {
				t.Errorf("stderr %q does not contain %q", r.stderr, c.want)
			}
			r.noAgent()
		})
	}
	if _, err := os.Stat(filepath.Join(f.dir, "none")); err == nil {
		t.Error("a --write file was created")
	}
}

func TestOpenFilesClosesWhatItOpenedOnError(t *testing.T) {
	f := newPassFixture(t)
	count := func() int {
		fds, err := hostListFDs()
		if err != nil {
			t.Fatal(err)
		}
		return len(fds)
	}
	before := count()
	if _, err := openFiles([]fdFile{{FD: 4, Path: f.a}, {FD: 5, Path: f.b, Write: true}, {FD: 6, Path: f.sub}}); err == nil {
		t.Fatal("a folder was opened for reading")
	}
	if after := count(); after != before {
		t.Errorf("%d descriptors before, %d after", before, after)
	}
	files, err := openFiles([]fdFile{{FD: 4, Path: f.a}, {FD: 5, Path: f.b, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range files {
		if fl, err := unix.FcntlInt(uintptr(o.fd), unix.F_GETFD, 0); err != nil || fl&unix.FD_CLOEXEC == 0 {
			t.Errorf("descriptor %d is not close-on-exec before placement", o.fd)
		}
		unix.Close(o.fd)
	}
}
