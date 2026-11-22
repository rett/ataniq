package noaicheck

import (
	"os"
	"path"
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
