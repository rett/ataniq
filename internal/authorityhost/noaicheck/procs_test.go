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
		{"a process that ended", func(f *fixture) { vanishing(f, "81") }, nil},
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
		{"a process that ended", func(f *fixture) { vanishing(f, "83") }, nil},
		{"processes without one identity", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.dir("proc/1")
			f.dir("proc/42")
			f.file("proc/net/tcp", tcpHeader)
		}, []Hit{inc(PathProc, DetailNoIdentity), inc(PathProcIdentity, "2")}},
		{"one identity does not cover folders that stay without a name", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc/7/comm", "bash\n")
			f.dir("proc/8")
			f.dir("proc/9")
		}, []Hit{inc(PathProcIdentity, "2")}},
		{"one identity among processes that ended", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc/7/comm", "bash\n")
			vanishing(f, "8")
			vanishing(f, "9")
		}, nil},
		{"an empty comm is no identity", func(f *fixture) {
			mustRemoveAll(f, "proc")
			f.file("proc/7/comm", "\n")
			f.file("proc/8/comm", "")
		}, []Hit{inc(PathProc, DetailNoIdentity), inc(PathProcIdentity, "2")}},
		{"an empty comm with an executable", func(f *fixture) {
			f.file("proc/7/comm", "")
			f.link("proc/7/exe", "/usr/bin/bash")
		}, nil},
		{"an empty exe target is no identity", func(f *fixture) {
			f.file("proc/7/comm", "\n")
			f.link("proc/7/exe", " (deleted)")
		}, []Hit{inc(PathProcIdentity, "1")}},
		{"a missing comm with an executable", func(f *fixture) {
			f.link("proc/7/exe", "/usr/bin/bash")
		}, nil},
	}, (*Checker).CheckProcesses)
}

// vanishing plants the process pid whose folder goes away while its comm
// is opened: a process that ends while it is read.
func vanishing(f *fixture, pid string) {
	f.t.Helper()
	f.file("proc/"+pid+"/comm", "short-lived\n")
	prev := testHookBeforeOpen
	testHookBeforeOpen = func(rel string) {
		if rel == "proc/"+pid+"/comm" {
			os.RemoveAll(f.abs("proc/" + pid))
		}
		if prev != nil {
			prev(rel)
		}
	}
	f.t.Cleanup(func() { testHookBeforeOpen = nil })
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
