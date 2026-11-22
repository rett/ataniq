package noaicheck

import (
	"os"
	"path"
	"reflect"
	"syscall"
	"testing"
)

// mkfifo replaces rel with a FIFO.
func mkfifo(f *fixture, rel string) {
	f.t.Helper()
	f.dir(path.Dir(rel))
	os.Remove(f.abs(rel))
	if err := syscall.Mkfifo(f.abs(rel), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func TestCheckProcesses(t *testing.T) {
	proc := func(p, n string) Hit { return Hit{Kind: KindProcess, Path: p, Detail: n} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"by comm", func(f *fixture) { f.file("proc/77/comm", "claude\n") }, []Hit{proc("/proc/77", "claude")}},
		{"by exe", func(f *fixture) {
			f.file("proc/78/comm", "node\n")
			f.link("proc/78/exe", "/home/user/.local/bin/codex")
		}, []Hit{proc("/proc/78", "codex")}},
		{"by a deleted exe", func(f *fixture) {
			f.file("proc/79/comm", "MainThread\n")
			f.link("proc/79/exe", "/usr/bin/codex (deleted)")
		}, []Hit{proc("/proc/79", "codex")}},
		{"a long name cut to 15 bytes", func(f *fixture) { f.file("proc/80/comm", "cursor-agent-lo\n") },
			[]Hit{proc("/proc/80", "cursor-agent-longname")}},
		{"a process that ended", func(f *fixture) { f.dir("proc/81") }, nil},
		{"not a process folder", func(f *fixture) { f.file("proc/sys/claude/comm", "claude\n") }, nil},
		{"a name that only starts like one", func(f *fixture) { f.file("proc/82/comm", "claudex\n") }, nil},
	}, (*Checker).CheckProcesses)
}

// The process check fails closed: no usable proc/ is incomplete, and so are
// processes whose identity is refused; a process that ended or a kernel
// thread is not.
func TestCheckProcessesFailsClosed(t *testing.T) {
	inc := func(p, d string) Hit { return Hit{Kind: KindIncomplete, Path: p, Detail: d} }
	runCases(t, []checkCase{
		{"no proc", func(f *fixture) { mustRemoveAll(f, "proc") }, []Hit{inc(PathProc, "missing")}},
		{"a proc without processes", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc/net/tcp", tcpHeader)
		}, []Hit{inc(PathProc, "no processes")}},
		{"proc is a file", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc", "x")
		}, []Hit{inc(PathProc, "not a directory")}},
		{"a kernel thread", func(f *fixture) { f.file("proc/2/comm", "kthreadd\n") }, nil},
		{"a process that ended", func(f *fixture) { f.dir("proc/83") }, nil},
		{"processes without one identity", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.dir("proc/1")
			f.dir("proc/42")
			f.file("proc/net/tcp", tcpHeader)
		}, []Hit{inc(PathProc, DetailNoIdentity)}},
		{"one identity among processes that ended", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc/7/comm", "bash\n")
			f.dir("proc/8")
			f.dir("proc/9")
		}, nil},
	}, (*Checker).CheckProcesses)
}

func TestCheckProcessesPermission(t *testing.T) {
	skipIfRoot(t)
	plant := func(f *fixture) {
		f.file("proc/50/comm", "secret-daemon\n")
		if err := os.Chmod(f.abs("proc/50/comm"), 0); err != nil {
			t.Fatal(err)
		}
		f.file("proc/51/comm", "other\n")
		f.link("proc/51/exe", "/usr/bin/other")
		if err := os.Chmod(f.abs("proc/51"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.t.Cleanup(func() { os.Chmod(f.abs("proc/51"), 0o755) })
	}
	for _, tc := range []struct {
		name string
		euid int
		want []Hit
	}{
		{"as a user: one count", 1000, []Hit{{Kind: KindIncomplete, Path: PathProcAccess, Detail: "2"}}},
		{"as root: one hit each", 0, []Hit{
			{Kind: KindUnreadable, Path: "/proc/50/comm", Detail: "permission denied"},
			{Kind: KindUnreadable, Path: "/proc/51/comm", Detail: "permission denied"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			plant(f)
			c := f.checker(testLists())
			c.euid = tc.euid
			within(t, c.CheckProcesses)
			if r := c.Report(); !reflect.DeepEqual(r.Hits, tc.want) || r.Complete && tc.euid != 0 {
				t.Errorf("hits %v, want %v", r.Hits, tc.want)
			}
		})
	}
}
