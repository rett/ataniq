package assert

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func TestRunInsideAllOK(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			s := testSpec(rt)
			rep := RunInside(s, goodSystem(s))
			if rep.Version != ReportVersion || rep.Digest != s.Digest {
				t.Errorf("report header %d %s", rep.Version, rep.Digest)
			}
			if len(rep.Results) != 8 {
				t.Fatalf("got %d results, want 8", len(rep.Results))
			}
			for i, res := range rep.Results {
				if res.Number != i+1 || res.ID != idOf(i+1) {
					t.Errorf("result %d is %d %s", i, res.Number, res.ID)
				}
				if res.Status != StatusOK {
					t.Errorf("assertion %d: %s: %s", res.Number, res.Status, res.Detail)
				}
			}
			if Failed(rep, s) {
				t.Errorf("Failed: %v", Problems(rep, s, RequireInside))
			}
			// The report survives the line format.
			back, err := ParseLine(EncodeLine(rep))
			if err != nil {
				t.Fatalf("ParseLine: %v", err)
			}
			if Failed(back, s) {
				t.Errorf("Failed after a round trip")
			}
		})
	}
}

func TestRunInsideDoesNotWriteOrDialTwice(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	RunInside(s, f)
	if got, wantDials := strings.Join(f.dialed, ","), "tcp 127.0.0.1:40123,unix @example-canary-1"; got != wantDials {
		t.Errorf("dialed %q, want %q", got, wantDials)
	}
}

// One case per way an assertion fails or cannot be checked.
func TestInsideAssertions(t *testing.T) {
	cases := []struct {
		name   string
		rt     profile.Runtime
		mutate func(s *Spec, f *fakeSys)
		n      int
		status Status
		detail string
	}{
		// 1
		{"1 holder socket exists", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(s.HolderPaths[0], "") }, 1, StatusFailed, `holder path "/run/example/holder.sock" exists`},
		{"1 holder state is a folder", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir(s.HolderPaths[2]) }, 1, StatusFailed, "/var/lib/example/holder"},
		{"1 mount point mentions a substring", profile.Claude, func(s *Spec, f *fakeSys) { f.setMount("/run/example/x", true) }, 1, StatusFailed, `mentions "/run/example"`},
		{"1 mount root mentions a substring", profile.Claude, func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{Root: "/home/owner/work/ataniq", Point: "/work/leak", Options: "rw", SuperOptions: "rw"})
		}, 1, StatusFailed, `mount "/work/leak" mentions "/home/owner"`},
		{"1 mount source mentions a substring", profile.Claude, func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{Root: "/", Point: "/work/leak", Source: "/run/example/dir", Options: "rw", SuperOptions: "rw"})
		}, 1, StatusFailed, "/work/leak"},
		{"1 no mount table", profile.Claude, func(s *Spec, f *fakeSys) { f.mountsErr = errors.New("no /proc") }, 1, StatusNotChecked, "no mount table"},
		{"1 holder path cannot be asked about", profile.Claude, func(s *Spec, f *fakeSys) { f.lstatErr[s.HolderPaths[1]] = syscall.EACCES }, 1, StatusNotChecked, "cannot tell whether it exists"},
		{"1 a parent is not a folder counts as absent", profile.Claude, func(s *Spec, f *fakeSys) { f.lstatErr[s.HolderPaths[1]] = syscall.ENOTDIR }, 1, StatusOK, ""},
		{"1 failed beats not-checked", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setFile(s.HolderPaths[0], "")
			f.lstatErr[s.HolderPaths[1]] = syscall.EACCES
		}, 1, StatusFailed, "1 more checks could not be made"},

		// 2
		{"2 extra variable", profile.Claude, func(s *Spec, f *fakeSys) { f.env = append(f.env, "SSH_AUTH_SOCK=/tmp/agent") }, 2, StatusFailed, `unexpected "SSH_AUTH_SOCK"`},
		{"2 missing variable", profile.Claude, func(s *Spec, f *fakeSys) { f.env = f.env[:2] }, 2, StatusFailed, `missing "TERM"`},
		{"2 changed value", profile.Claude, func(s *Spec, f *fakeSys) { f.env[1] = "PATH=/tmp/bin" }, 2, StatusFailed, `changed "PATH"`},
		{"2 duplicate", profile.Claude, func(s *Spec, f *fakeSys) { f.env = append(f.env, "HOME=/home/lead") }, 2, StatusFailed, `duplicate "HOME"`},
		{"2 malformed entry", profile.Claude, func(s *Spec, f *fakeSys) { f.env = append(f.env, "NOEQUALS") }, 2, StatusFailed, "malformed"},
		{"2 an unexpected value is not quoted", profile.Claude, func(s *Spec, f *fakeSys) { f.env = append(f.env, "SECRET_THING=hunter2") }, 2, StatusFailed, "unexpected"},

		// 3
		{"3 tcp connects", profile.Claude, func(s *Spec, f *fakeSys) { f.dialErr["tcp "+s.LoopbackTCP] = nil }, 3, StatusFailed, "host loopback port 127.0.0.1:40123 is reachable"},
		{"3 abstract connects", profile.Claude, func(s *Spec, f *fakeSys) { f.dialErr["unix @"+s.AbstractSocket] = nil }, 3, StatusFailed, "host abstract socket @example-canary-1 is reachable"},
		{"3 network unreachable is ok", profile.Claude, func(s *Spec, f *fakeSys) { f.dialErr["tcp "+s.LoopbackTCP] = syscall.ENETUNREACH }, 3, StatusOK, ""},
		{"3 timeout is ok", profile.Claude, func(s *Spec, f *fakeSys) { f.dialErr["tcp "+s.LoopbackTCP] = timeoutErr{} }, 3, StatusOK, ""},
		{"3 out of descriptors is not-checked", profile.Claude, func(s *Spec, f *fakeSys) { f.dialErr["tcp "+s.LoopbackTCP] = syscall.EMFILE }, 3, StatusNotChecked, "does not show it is unreachable"},
		{"3 one reachable and one not asked is failed", profile.Claude, func(s *Spec, f *fakeSys) {
			f.dialErr["tcp "+s.LoopbackTCP] = nil
			f.dialErr["unix @"+s.AbstractSocket] = syscall.ENOMEM
		}, 3, StatusFailed, "is reachable"},

		// 4
		{"4 does not resolve", profile.Claude, func(s *Spec, f *fakeSys) { f.lookupErr = errors.New("no such host") }, 4, StatusFailed, "does not resolve"},
		{"4 no address", profile.Claude, func(s *Spec, f *fakeSys) { f.hosts = nil }, 4, StatusFailed, "no address"},

		// 5
		{"5 sysctl 1 and no filter", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(tiocstiSysctl, "1\n") }, 5, StatusFailed, "no seccomp filter is loaded"},
		{"5 sysctl 1 and a filter", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setFile(tiocstiSysctl, "1\n")
			f.setFile(procStatus, "Seccomp:\t2\n")
		}, 5, StatusOK, "a seccomp filter is loaded"},
		{"5 sysctl absent and no filter", profile.Claude, func(s *Spec, f *fakeSys) { delete(f.nodes, tiocstiSysctl) }, 5, StatusFailed, "absent"},
		{"5 sysctl absent and a filter", profile.Claude, func(s *Spec, f *fakeSys) {
			delete(f.nodes, tiocstiSysctl)
			f.setFile(procStatus, "Seccomp:\t2\n")
		}, 5, StatusOK, ""},
		{"5 sysctl unreadable and no filter", profile.Claude, func(s *Spec, f *fakeSys) { f.readErr[tiocstiSysctl] = syscall.EACCES }, 5, StatusNotChecked, "cannot be read"},
		{"5 sysctl non-zero and status unreadable", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setFile(tiocstiSysctl, "1\n")
			f.readErr[procStatus] = syscall.EACCES
		}, 5, StatusNotChecked, "cannot be read"},
		{"5 sysctl 0 is enough without status", profile.Claude, func(s *Spec, f *fakeSys) { f.readErr[procStatus] = syscall.EACCES }, 5, StatusOK, "legacy_tiocsti is 0"},
		{"5 sysctl is garbage", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(tiocstiSysctl, "zero") }, 5, StatusFailed, `"zero"`},
		{"5 filter expected and loaded", profile.Claude, func(s *Spec, f *fakeSys) {
			s.SeccompExpected = true
			f.setFile(procStatus, "Seccomp:\t2\n")
		}, 5, StatusOK, "as the profile expects"},
		{"5 filter expected and not loaded, sysctl 0", profile.Claude, func(s *Spec, f *fakeSys) { s.SeccompExpected = true }, 5, StatusFailed, "Seccomp: is 0"},
		{"5 filter expected, strict mode is not a filter", profile.Claude, func(s *Spec, f *fakeSys) {
			s.SeccompExpected = true
			f.setFile(procStatus, "Seccomp:\t1\n")
		}, 5, StatusFailed, "not 2"},
		{"5 filter expected and status unreadable", profile.Claude, func(s *Spec, f *fakeSys) {
			s.SeccompExpected = true
			f.readErr[procStatus] = syscall.EACCES
		}, 5, StatusNotChecked, procStatus},
		{"5 status line is not a number", profile.Claude, func(s *Spec, f *fakeSys) {
			s.SeccompExpected = true
			f.setFile(procStatus, "Seccomp:\tmaybe\n")
		}, 5, StatusNotChecked, "not a number"},
		{"5 Seccomp_filters is not the mode", profile.Claude, func(s *Spec, f *fakeSys) {
			s.SeccompExpected = true
			f.setFile(procStatus, "Seccomp_filters:\t2\n")
		}, 5, StatusFailed, "Seccomp: is 0"},

		// 6
		{"6 owner folder exists", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir("/home/owner/.claude") }, 6, StatusFailed, `owner path "/home/owner/.claude" exists`},
		{"6 ssh exists", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir("/home/owner/.ssh") }, 6, StatusFailed, ".ssh"},
		{"6 checkout exists", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir("/home/owner/work/ataniq") }, 6, StatusFailed, "ataniq"},
		{"6 other config folder absent", profile.Claude, func(s *Spec, f *fakeSys) {}, 6, StatusOK, "does not exist"},
		{"6 other config folder made by the lead", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir("/home/lead/.codex") }, 6, StatusOK, "not a failure of the boundary"},
		{"6 other config folder is a mount point", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setDir("/home/lead/.codex")
			f.setMount("/home/lead/.codex", false)
		}, 6, StatusFailed, "something is mounted at or below"},
		{"6 mount below the other config folder", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setDir("/home/lead/.codex")
			f.setMount("/home/lead/.codex/skills", true)
		}, 6, StatusFailed, "/home/lead/.codex/skills"},
		{"6 a mount of a sibling is not a mount there", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setDir("/home/lead/.codex")
			f.setMount("/home/lead/.codex-other", true)
		}, 6, StatusOK, "not a failure of the boundary"},
		{"6 other folder and no mount table", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setDir("/home/lead/.codex")
			f.mountsErr = errors.New("gone")
		}, 6, StatusNotChecked, "no mount table"},
		{"6 other folder cannot be asked about", profile.Claude, func(s *Spec, f *fakeSys) { f.lstatErr["/home/lead/.codex"] = syscall.EACCES }, 6, StatusNotChecked, "cannot tell"},
		{"6 codex lead: the claude folder is the other one", profile.Codex, func(s *Spec, f *fakeSys) {
			f.setDir("/home/lead/.claude")
			f.setMount("/home/lead/.claude", false)
		}, 6, StatusFailed, "/home/lead/.claude"},

		// 7
		{"7 no .git", profile.Claude, func(s *Spec, f *fakeSys) { delete(f.nodes, s.Tree+"/.git") }, 7, StatusFailed, "not a clone"},
		{"7 .git is a file", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(s.Tree+"/.git", "gitdir: /elsewhere\n") }, 7, StatusFailed, "not a folder"},
		{"7 .git is a link", profile.Claude, func(s *Spec, f *fakeSys) {
			f.nodes[s.Tree+"/.git"] = fakeNode{info: FileInfo{Mode: fs.ModeSymlink | 0o777}}
		}, 7, StatusFailed, "a symbolic link"},
		{"7 config on a writable mount", profile.Claude, func(s *Spec, f *fakeSys) { f.setMount(s.GitReadOnly[0], false) /* stacked on top */ }, 7, StatusFailed, "writable mount"},
		{"7 hooks not covered by their own mount", profile.Claude, func(s *Spec, f *fakeSys) {
			mounts := f.mounts[:0]
			for _, m := range f.mounts {
				if m.Point != s.GitReadOnly[1] {
					mounts = append(mounts, m)
				}
			}
			f.mounts = mounts
		}, 7, StatusFailed, `"/work/ataniq/.git/hooks" is on a writable mount at "/work/ataniq"`},
		{"7 access says writable on a read-only mount", profile.Claude, func(s *Spec, f *fakeSys) { f.writable[s.GitReadOnly[2]] = true }, 7, StatusFailed, "writable by access(W_OK)"},
		{"7 info is missing", profile.Claude, func(s *Spec, f *fakeSys) { delete(f.nodes, s.GitReadOnly[2]) }, 7, StatusFailed, "does not exist"},
		{"7 hooks is a regular file", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(s.GitReadOnly[1], "") }, 7, StatusFailed, "a regular file"},
		{"7 config is a folder", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir(s.GitReadOnly[0]) }, 7, StatusFailed, "a folder"},
		{"7 no mount table", profile.Claude, func(s *Spec, f *fakeSys) { f.mountsErr = errors.New("gone") }, 7, StatusNotChecked, "no mount table"},
		{"7 access cannot be asked", profile.Claude, func(s *Spec, f *fakeSys) { f.accessErr[s.GitReadOnly[0]] = syscall.ENOSYS }, 7, StatusNotChecked, "access(W_OK)"},

		// 8
		{"8 missing", profile.Claude, func(s *Spec, f *fakeSys) { delete(f.nodes, s.ManagedSettings) }, 8, StatusFailed, "does not exist"},
		{"8 a link", profile.Claude, func(s *Spec, f *fakeSys) {
			f.nodes[s.ManagedSettings] = fakeNode{info: FileInfo{Mode: fs.ModeSymlink | 0o777}}
		}, 8, StatusFailed, "a symbolic link"},
		{"8 a folder", profile.Claude, func(s *Spec, f *fakeSys) { f.setDir(s.ManagedSettings) }, 8, StatusFailed, "a folder"},
		{"8 writable mount", profile.Claude, func(s *Spec, f *fakeSys) { f.setMount(s.ManagedSettings, false) }, 8, StatusFailed, "writable mount"},
		{"8 access says writable", profile.Claude, func(s *Spec, f *fakeSys) { f.writable[s.ManagedSettings] = true }, 8, StatusFailed, "access(W_OK)"},
		{"8 marker missing", profile.Claude, func(s *Spec, f *fakeSys) { f.setFile(s.ManagedSettings, `{"hooks":{}}`) }, 8, StatusFailed, "does not contain the marker"},
		{"8 too large", profile.Claude, func(s *Spec, f *fakeSys) {
			f.setFile(s.ManagedSettings, strings.Repeat("x", MaxManagedSettingsBytes+1))
		}, 8, StatusFailed, "larger than"},
		{"8 not regular on read", profile.Claude, func(s *Spec, f *fakeSys) { f.readErr[s.ManagedSettings] = ErrNotRegular }, 8, StatusFailed, "not a regular file"},
		{"8 read error", profile.Claude, func(s *Spec, f *fakeSys) { f.readErr[s.ManagedSettings] = syscall.EACCES }, 8, StatusNotChecked, "cannot be read"},
		{"8 lstat error", profile.Claude, func(s *Spec, f *fakeSys) { f.lstatErr[s.ManagedSettings] = syscall.EACCES }, 8, StatusNotChecked, "permission denied"},
		{"8 codex without marker", profile.Codex, func(s *Spec, f *fakeSys) {}, 8, StatusOK, "no marker is required for codex"},
		{"8 codex with a marker that is missing", profile.Codex, func(s *Spec, f *fakeSys) { s.ManagedMarker = "needed" }, 8, StatusFailed, "does not contain the marker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testSpec(tc.rt)
			f := goodSystem(s)
			tc.mutate(&s, f)
			rep := RunInside(s, f)
			want(t, rep, tc.n, tc.status, tc.detail)
			// Only the assertion under test changes its result, except where
			// the mutation shares a source (the mount table, the spec).
			if tc.status != StatusOK && !Failed(rep, s) {
				t.Errorf("a %s result did not make the report fail", tc.status)
			}
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRunInsideSecretIsNotQuoted(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	f.env = append(f.env, "SECRET_THING=hunter2")
	rep := RunInside(s, f)
	if d := find(t, rep, 2).Detail; strings.Contains(d, "hunter2") {
		t.Errorf("detail quotes the value of an unexpected variable: %q", d)
	}
}

func TestRunInsideDetailsAreEscaped(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	f.env = append(f.env, "EVIL\x1b[2J\nINJECT=1")
	f.mounts = append(f.mounts, Mount{Point: "/run/example/\x1b]0;x\x07\nATANIQ-PROBE-REPORT {}", Options: "rw", SuperOptions: "rw"})
	rep := RunInside(s, f)
	line := EncodeLine(rep)
	if strings.Count(string(line), "\n") != 1 {
		t.Fatalf("report is not one line: %q", line)
	}
	for _, res := range rep.Results {
		if !cleanDetail(res.Detail) {
			t.Errorf("assertion %d detail is not clean: %q", res.Number, res.Detail)
		}
	}
	if _, err := ParseLine(line); err != nil {
		t.Errorf("ParseLine: %v", err)
	}
}

func TestRunInsideRefusedSpec(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	s.Tree = "relative"
	rep := RunInside(s, f)
	if len(rep.Results) != 8 {
		t.Fatalf("got %d results", len(rep.Results))
	}
	for _, res := range rep.Results {
		if res.Status != StatusNotChecked || !strings.Contains(res.Detail, "spec is refused") {
			t.Errorf("assertion %d: %s %s", res.Number, res.Status, res.Detail)
		}
	}
	// Even a spec that lists everything as optional does not allow it.
	s.Optional = []int{1, 2, 3, 4, 5, 6, 7, 8}
	if !Failed(rep, s) {
		t.Errorf("a refused spec allowed a start")
	}
}

func TestNotCheckedFailsUnlessOptional(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	f.mountsErr = errors.New("gone")
	rep := RunInside(s, f)
	var notChecked []int
	for _, res := range rep.Results {
		if res.Status == StatusNotChecked {
			notChecked = append(notChecked, res.Number)
		}
		if res.Status == StatusFailed {
			t.Errorf("assertion %d failed: %s", res.Number, res.Detail)
		}
	}
	if len(notChecked) == 0 {
		t.Fatal("nothing was not-checked")
	}
	if !Failed(rep, s) {
		t.Fatal("not-checked results did not fail the report")
	}
	s.Optional = notChecked[:len(notChecked)-1]
	if !Failed(rep, s) {
		t.Fatal("one assertion still not-checked and not optional, but the report passed")
	}
	s.Optional = notChecked
	if Failed(rep, s) {
		t.Fatalf("every not-checked assertion is optional: %v", Problems(rep, s, RequireInside))
	}
	// A failed result is a failure even when optional.
	rep.Results[notChecked[0]-1].Status = StatusFailed
	if !Failed(rep, s) {
		t.Fatal("a failed optional assertion passed")
	}
}

func TestReadOnlyNeedsBothMountAndAccess(t *testing.T) {
	s := testSpec(profile.Claude)
	for _, tc := range []struct {
		name     string
		roMount  bool
		writable bool
		want     Status
	}{
		{"read-only mount, access refuses", true, false, StatusOK},
		{"read-only mount, access allows", true, true, StatusFailed},
		{"writable mount, access refuses", false, false, StatusFailed},
		{"writable mount, access allows", false, true, StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := goodSystem(s)
			f.mounts = f.mounts[:3] // drop the specific mounts
			f.setMount(s.ManagedSettings, tc.roMount)
			f.writable[s.ManagedSettings] = tc.writable
			rep := RunInside(s, f)
			if got := find(t, rep, 8).Status; got != tc.want {
				t.Errorf("status %s, want %s: %s", got, tc.want, find(t, rep, 8).Detail)
			}
		})
	}
}

func TestSuperblockReadOnlyCounts(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	f.mounts = f.mounts[:3]
	f.mounts = append(f.mounts, Mount{Point: s.ManagedSettings, Options: "rw", SuperOptions: "ro,errors=remount-ro"})
	if got := find(t, RunInside(s, f), 8); got.Status != StatusOK {
		t.Errorf("%s %s", got.Status, got.Detail)
	}
}

func TestMountFor(t *testing.T) {
	mounts := []Mount{
		{Point: "/"},
		{Point: "/work/ataniq", Options: "rw"},
		{Point: "/work/ataniq/.git/info", Options: "ro"},
		{Point: "/work/ataniq/.git/info", Options: "rw"}, // stacked on top
	}
	for _, tc := range []struct {
		path, point, opts string
		ok                bool
	}{
		{"/etc/passwd", "/", "", true},
		{"/work/ataniq/x", "/work/ataniq", "rw", true},
		{"/work/ataniq2/x", "/", "", true}, // a name prefix is not a parent
		{"/work/ataniq/.git/info", "/work/ataniq/.git/info", "rw", true},
		{"/work/ataniq/.git/info/exclude", "/work/ataniq/.git/info", "rw", true},
	} {
		m, ok := mountFor(mounts, tc.path)
		if ok != tc.ok || m.Point != tc.point || m.Options != tc.opts {
			t.Errorf("mountFor(%s) = %+v %v, want %s %s", tc.path, m, ok, tc.point, tc.opts)
		}
	}
	if _, ok := mountFor(mounts[1:], "/etc"); ok {
		t.Error("a path with no mount above it found one")
	}
}

func TestProblemsRequireAll(t *testing.T) {
	s := testSpec(profile.Claude)
	rep := RunInside(s, goodSystem(s))
	if !FailedAll(rep, s) {
		t.Fatal("the inside report alone must not satisfy RequireAll")
	}
	if p := Problems(rep, s, RequireAll); len(p) != 4 {
		t.Errorf("want 4 missing outside results, got %v", p)
	}
}

func TestInsideHasNoExec(t *testing.T) {
	// The inside checks never start a process.
	assertNoImport(t, "os/exec")
}

func TestDetailListsAFewFindingsAndCountsTheRest(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	for _, p := range s.OwnerPaths {
		f.setDir(p)
	}
	d := find(t, RunInside(s, f), 6).Detail
	if !strings.Contains(d, "and 2 more") || strings.Count(d, "exists") != 3 {
		t.Errorf("detail %q", d)
	}
}
