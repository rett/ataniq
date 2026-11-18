package assert

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// The identities of the coordinators' folders in testSpec.
var (
	coordRoot = FileIdentity{Device: 9, Inode: 90}
	coordTree = FileIdentity{Device: 9, Inode: 91}
)

// assertion11 runs the inside checks on the good system after mutate and
// returns the report and the result of assertion 11.
func assertion11(t *testing.T, mutate func(s *Spec, f *fakeSys)) (Report, Spec, Result) {
	t.Helper()
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	if mutate != nil {
		mutate(&s, f)
	}
	rep := RunInside(s, f)
	return rep, s, find(t, rep, 11)
}

// dropMount removes the mounts at a point from the fake's table.
func dropMount(f *fakeSys, point string) {
	kept := f.mounts[:0]
	for _, m := range f.mounts {
		if m.Point != point {
			kept = append(kept, m)
		}
	}
	f.mounts = kept
}

// remount changes whether the mounts at a point are read-only.
func remount(f *fakeSys, point string, ro bool) {
	for i, m := range f.mounts {
		if m.Point == point {
			f.mounts[i].Options = "rw,nosuid"
			if ro {
				f.mounts[i].Options = "ro,nosuid"
			}
		}
	}
}

func symlinkNode() fakeNode { return fakeNode{info: FileInfo{Mode: fs.ModeSymlink | 0o777}} }

func TestAssertion11OK(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		s := testSpec(rt)
		f := goodSystem(s)
		// What a project's coordinators publish, for two features.
		for _, feature := range []string{"feature-a", "feature-b"} {
			for _, name := range []string{"qargi", "out", "archive"} {
				f.setDir(profile.CoordInside + "/" + feature + "/" + name)
			}
			f.setDir(profile.CoordInside + "/" + feature)
		}
		r := find(t, RunInside(s, f), 11)
		if r.Status != StatusOK || r.ID != IDParentFolders {
			t.Fatalf("%s: %+v", rt, r)
		}
		for _, want := range []string{
			"the four parent folders are present, each a mount of its own",
			"/ataniq/requests and /ataniq/mail are writable, /ataniq/mailbox and /coord read-only",
			"none of 2 coordinator paths exists",
			"are none of the 2 of the coordinators' folders",
			"none of 2 entries of /coord holds a tree or a home",
		} {
			if !strings.Contains(r.Detail, want) {
				t.Errorf("%s: detail %q lacks %q", rt, r.Detail, want)
			}
		}
		if !cleanDetail(r.Detail) {
			t.Errorf("the detail is cut or not clean: %q", r.Detail)
		}
	}
}

// One case per way assertion 11 fails or cannot be checked.
func TestAssertion11(t *testing.T) {
	coords := "/var/lib/example/state/coords"
	cases := []struct {
		name   string
		mutate func(s *Spec, f *fakeSys)
		status Status
		detail string
	}{
		// The four folders are present.
		{"the requests folder is missing", func(s *Spec, f *fakeSys) { delete(f.nodes, profile.RequestsInside) }, StatusFailed, `the requests folder "/ataniq/requests" does not exist`},
		{"the mailbox is missing", func(s *Spec, f *fakeSys) { delete(f.nodes, profile.MailboxInside) }, StatusFailed, `the mailbox "/ataniq/mailbox" does not exist`},
		{"the mail folder is missing", func(s *Spec, f *fakeSys) { delete(f.nodes, profile.MailInside) }, StatusFailed, `the mail folder "/ataniq/mail" does not exist`},
		{"the published folder is missing", func(s *Spec, f *fakeSys) { delete(f.nodes, profile.CoordInside) }, StatusFailed, `the published folder "/coord" does not exist`},
		{"a parent of a folder is not a folder", func(s *Spec, f *fakeSys) { f.lstatErr[profile.MailInside] = syscall.ENOTDIR }, StatusFailed, "does not exist"},
		{"the mailbox is a regular file", func(s *Spec, f *fakeSys) { f.setFile(profile.MailboxInside, "") }, StatusFailed, "is a regular file, not a folder"},
		{"the published folder is a link", func(s *Spec, f *fakeSys) { f.nodes[profile.CoordInside] = symlinkNode() }, StatusFailed, `"/coord" is a symbolic link, not a folder`},
		{"a folder cannot be asked about", func(s *Spec, f *fakeSys) { f.lstatErr[profile.RequestsInside] = syscall.EACCES }, StatusNotChecked, "permission denied"},
		{"a missing folder beats a question not asked", func(s *Spec, f *fakeSys) {
			delete(f.nodes, profile.MailInside)
			f.lstatErr[profile.RequestsInside] = syscall.EACCES
		}, StatusFailed, "more checks could not be made"},

		// Each is a mount of its own.
		{"the requests folder is a plain folder of the root", func(s *Spec, f *fakeSys) { dropMount(f, profile.RequestsInside) }, StatusFailed, `"/ataniq/requests" is not a mount of its own: it is part of the mount at "/"`},
		{"the published folder is a plain folder of a mount above", func(s *Spec, f *fakeSys) {
			dropMount(f, profile.CoordInside)
			// Read-only, and still not the bound folder.
			f.mounts[0].Options = "ro"
		}, StatusFailed, `"/coord" is not a mount of its own`},

		// The mailbox and the published folder are not writable.
		{"the mailbox is on a writable mount", func(s *Spec, f *fakeSys) { f.setMount(profile.MailboxInside, false) /* stacked on top */ }, StatusFailed, `the mailbox "/ataniq/mailbox" is on a writable mount`},
		{"the published folder is on a writable mount", func(s *Spec, f *fakeSys) { f.setMount(profile.CoordInside, false) }, StatusFailed, `the published folder "/coord" is on a writable mount`},
		{"access says the mailbox is writable", func(s *Spec, f *fakeSys) { f.writable[profile.MailboxInside] = true }, StatusFailed, `"/ataniq/mailbox" is writable by access(W_OK)`},
		{"access says the published folder is writable", func(s *Spec, f *fakeSys) { f.writable[profile.CoordInside] = true }, StatusFailed, `"/coord" is writable by access(W_OK)`},
		{"a read-write mount below the published folder", func(s *Spec, f *fakeSys) { f.setMount(profile.CoordInside+"/feature-a/out", false) }, StatusFailed, `has a writable mount at or below it: "/coord/feature-a/out"`},
		{"a read-only mount below the published folder is no failure", func(s *Spec, f *fakeSys) { f.setMount(profile.CoordInside+"/feature-a/out", true) }, StatusOK, ""},
		{"a later read-write bind hides the read-only mailbox", func(s *Spec, f *fakeSys) {
			f.setMount("/ataniq", false)
			f.mountIDs[profile.MailboxInside] = f.mounts[len(f.mounts)-1].ID
		}, StatusFailed, "writable mount"},
		{"access cannot be asked of the mailbox", func(s *Spec, f *fakeSys) { f.accessErr[profile.MailboxInside] = syscall.ENOSYS }, StatusNotChecked, "access(W_OK)"},

		// The requests and the mail folder are writable.
		{"the requests folder is on a read-only mount", func(s *Spec, f *fakeSys) { f.setMount(profile.RequestsInside, true) }, StatusFailed, `the requests folder "/ataniq/requests" is on a read-only mount`},
		{"the mail folder is on a read-only file system", func(s *Spec, f *fakeSys) {
			dropMount(f, profile.MailInside)
			f.mounts = append(f.mounts, Mount{ID: 700, Point: profile.MailInside, Options: "rw", SuperOptions: "ro"})
		}, StatusFailed, `the mail folder "/ataniq/mail" is on a read-only mount`},
		{"access refuses the requests folder", func(s *Spec, f *fakeSys) { f.writable[profile.RequestsInside] = false }, StatusFailed, `"/ataniq/requests" is not writable by access(W_OK)`},
		{"access refuses the mail folder", func(s *Spec, f *fakeSys) { f.writable[profile.MailInside] = false }, StatusFailed, `"/ataniq/mail" is not writable by access(W_OK)`},
		{"access cannot be asked of the mail folder", func(s *Spec, f *fakeSys) { f.accessErr[profile.MailInside] = syscall.ENOSYS }, StatusNotChecked, "access(W_OK)"},

		// Questions about the mounts that cannot be asked.
		{"no mount table", func(s *Spec, f *fakeSys) { f.mountsErr = errors.New("gone") }, StatusNotChecked, "no mount table"},
		{"the kernel gives no mount ID", func(s *Spec, f *fakeSys) { f.noMountID = true }, StatusNotChecked, "no mount ID"},
		{"the mount ID is not in the table", func(s *Spec, f *fakeSys) { f.mountIDs[profile.MailInside] = 424242 }, StatusNotChecked, "424242 is not in the mount table"},

		// No coordinator path exists inside.
		{"the coordinators' root exists", func(s *Spec, f *fakeSys) { f.setDir(coords) }, StatusFailed, `coordinator path "/var/lib/example/state/coords" exists`},
		{"the shared login root exists", func(s *Spec, f *fakeSys) { f.setDir(s.CoordinatorPaths[1]) }, StatusFailed, `coordinator path "/var/lib/example/state/coord-login" exists`},
		{"a coordinator path cannot be asked about", func(s *Spec, f *fakeSys) { f.lstatErr[coords] = syscall.EACCES }, StatusNotChecked, "cannot tell whether it exists"},

		// No mount is related to one by the text of the table.
		{"a mount root is a coordinator's tree", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: coords + "/qualproj/feature-a/tree", Point: "/leak", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, `mount "/leak" has root "/var/lib/example/state/coords/qualproj/feature-a/tree", which is at, above or below the coordinator path`},
		{"a mount root is a coordinator's home", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: coords + "/qualproj/feature-a/home", Point: "/leak", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, "feature-a/home"},
		{"a mount root is a shared login", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/var/lib/example/state/coord-login/qualproj/claude", Point: "/leak", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, `the coordinator path "/var/lib/example/state/coord-login"`},
		{"a mount root is the state root above them", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/var/lib/example/state", Point: "/leak", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, `root "/var/lib/example/state"`},
		{"a mount source is below the coordinators' root", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/", Point: "/leak", Source: coords + "/img", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, `source "/var/lib/example/state/coords/img"`},
		{"a mount point is above the coordinators' root", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/", Point: "/var/lib", Source: "tmpfs", Options: "ro", SuperOptions: "rw"})
		}, StatusFailed, `mount point "/var/lib"`},
		{"the lead's own folders of the same state root are not related", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts,
				Mount{ID: 900, Root: "/var/lib/example/state/leads/arc-lead/home", Point: "/a", Source: "/dev/example1", Options: "rw", SuperOptions: "rw"},
				Mount{ID: 901, Root: "/var/lib/example/state/pub/qualproj", Point: "/b", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"},
				Mount{ID: 902, Root: "/var/lib/example/state/coords-old", Point: "/c", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"},
				Mount{ID: 903, Root: "/var/lib/example/state/coord-login-two", Point: "/d", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
		}, StatusOK, ""},

		// No mount point, parent folder or published entry has a
		// coordinator's identity: a mount root that is relative to its own file
		// system (a btrfs subvolume, a separate file system) matches no text.
		{"a bind of a coordinator's tree from a subvolume", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/@state/coords/qualproj/feature-a/tree", Point: "/leak", FSType: "btrfs", Source: "/dev/example1", Options: "ro", SuperOptions: "rw"})
			f.identities["/leak"] = coordTree
		}, StatusFailed, `mount point "/leak" has the identity 9:91 of a coordinator's folder or of one of its ancestors`},
		{"the published folder is the coordinators' root", func(s *Spec, f *fakeSys) { f.identities[profile.CoordInside] = coordRoot }, StatusFailed, `"/coord" has the identity 9:90`},
		{"the mail folder is a coordinator's folder", func(s *Spec, f *fakeSys) { f.identities[profile.MailInside] = coordTree }, StatusFailed, `the mail folder "/ataniq/mail" has the identity 9:91`},
		{"a published entry is a coordinator's folder", func(s *Spec, f *fakeSys) {
			f.setDir(profile.CoordInside + "/feature-a")
			f.identities[profile.CoordInside+"/feature-a"] = coordTree
		}, StatusFailed, `the published entry "/coord/feature-a" has the identity 9:91`},
		{"no coordinator identity is listed", func(s *Spec, f *fakeSys) { s.CoordinatorIdentities = nil }, StatusNotChecked, "the spec lists no coordinator identity"},
		{"an empty list of coordinator identities", func(s *Spec, f *fakeSys) { s.CoordinatorIdentities = []FileIdentity{} }, StatusNotChecked, "the spec lists no coordinator identity"},
		{"the identity of a parent folder cannot be read", func(s *Spec, f *fakeSys) { f.identityErr[profile.CoordInside] = syscall.EACCES }, StatusNotChecked, "identity"},
		{"an owner's identity is not a coordinator's", func(s *Spec, f *fakeSys) {
			// The mount point is failed by assertion 1, not by this one.
			f.setMount("/leak", true)
			f.identities["/leak"] = ownerHome
		}, StatusOK, ""},

		// No entry of the published folder holds a tree or a home.
		{"a published entry holds a tree", func(s *Spec, f *fakeSys) { f.setDir(profile.CoordInside + "/feature-a/tree") }, StatusFailed, `the published entry "/coord/feature-a" holds "tree": a coordinator's tree is visible`},
		{"a published entry holds a home", func(s *Spec, f *fakeSys) { f.setDir(profile.CoordInside + "/feature-a/home") }, StatusFailed, `the published entry "/coord/feature-a" holds "home": a coordinator's home is visible`},
		{"a tree that is a link is still a tree", func(s *Spec, f *fakeSys) {
			f.setDir(profile.CoordInside + "/feature-a")
			f.nodes[profile.CoordInside+"/feature-a/tree"] = symlinkNode()
		}, StatusFailed, "a coordinator's tree is visible"},
		{"the second entry holds a home", func(s *Spec, f *fakeSys) {
			f.setDir(profile.CoordInside + "/feature-a/out")
			f.setDir(profile.CoordInside + "/feature-b/home")
		}, StatusFailed, `"/coord/feature-b" holds "home"`},
		{"what is published is no failure", func(s *Spec, f *fakeSys) {
			for _, name := range []string{"qargi", "out", "archive"} {
				f.setDir(profile.CoordInside + "/feature-a/" + name)
			}
		}, StatusOK, "none of 1 entries of /coord holds a tree or a home"},
		{"a tree two levels down is not looked for", func(s *Spec, f *fakeSys) { f.setDir(profile.CoordInside + "/feature-a/out/tree") }, StatusOK, ""},
		{"an entry that is a file holds nothing", func(s *Spec, f *fakeSys) {
			f.setFile(profile.CoordInside+"/README", "")
			f.lstatErr[profile.CoordInside+"/README/tree"] = syscall.ENOTDIR
			f.lstatErr[profile.CoordInside+"/README/home"] = syscall.ENOTDIR
		}, StatusOK, "none of 1 entries"},
		{"the published folder cannot be listed", func(s *Spec, f *fakeSys) { f.namesErr[profile.CoordInside] = syscall.EACCES }, StatusNotChecked, `"/coord" cannot be listed`},
		{"the published folder holds too many entries", func(s *Spec, f *fakeSys) {
			for i := 0; i <= MaxCoordEntries; i++ {
				f.setDir(fmt.Sprintf("%s/f%d", profile.CoordInside, i))
			}
		}, StatusNotChecked, "holds more than 4096 entries: none was looked at"},
		{"an entry cannot be asked about", func(s *Spec, f *fakeSys) {
			f.setDir(profile.CoordInside + "/feature-a")
			f.lstatErr[profile.CoordInside+"/feature-a/home"] = syscall.EACCES
		}, StatusNotChecked, `"/coord/feature-a/home": cannot tell whether it exists`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, s, r := assertion11(t, tc.mutate)
			if r.Status != tc.status || !strings.Contains(r.Detail, tc.detail) {
				t.Fatalf("%s: %s, want %s with %q", r.Status, r.Detail, tc.status, tc.detail)
			}
			// A result that is not ok is a problem: assertion 11 is never
			// optional.
			if tc.status != StatusOK && !Failed(rep, s) {
				t.Errorf("a %s result did not make the report fail", tc.status)
			}
		})
	}
}

// As many entries as the bound are all looked at.
func TestAssertion11ListsAsManyEntriesAsTheBound(t *testing.T) {
	_, _, r := assertion11(t, func(s *Spec, f *fakeSys) {
		for i := 0; i < MaxCoordEntries; i++ {
			f.setDir(fmt.Sprintf("%s/f%d", profile.CoordInside, i))
		}
		f.setDir(fmt.Sprintf("%s/f%d/home", profile.CoordInside, MaxCoordEntries-1))
	})
	if r.Status != StatusFailed || !strings.Contains(r.Detail, "a coordinator's home is visible") {
		t.Errorf("%+v", r)
	}
}

// Writable needs both the mount and access to say so, as read-only does.
func TestReadWriteNeedsBothMountAndAccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		roMount  bool
		writable bool
		want     Status
	}{
		{"writable mount, access allows", false, true, StatusOK},
		{"writable mount, access refuses", false, false, StatusFailed},
		{"read-only mount, access allows", true, true, StatusFailed},
		{"read-only mount, access refuses", true, false, StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, r := assertion11(t, func(s *Spec, f *fakeSys) {
				remount(f, profile.RequestsInside, tc.roMount)
				f.writable[profile.RequestsInside] = tc.writable
			})
			if r.Status != tc.want {
				t.Errorf("status %s, want %s: %s", r.Status, tc.want, r.Detail)
			}
		})
	}
}

// Assertion 11 is a boundary fact: the spec cannot list it as optional, and a
// result that is not ok never allows a start.
func TestAssertion11IsNeverOptional(t *testing.T) {
	s := testSpec(profile.Claude)
	s.Optional = []int{11}
	if err := s.Validate(); err == nil {
		t.Error("assertion 11 was accepted as optional")
	}
	rep, s, r := assertion11(t, func(s *Spec, f *fakeSys) { s.CoordinatorIdentities = nil })
	if r.Status != StatusNotChecked || !Failed(rep, s) {
		t.Errorf("a not-checked assertion 11 allowed a start: %+v", r)
	}
	// A report without the result is a problem too.
	rep.Results = rep.Results[:len(rep.Results)-1]
	if p := Problems(rep, s, RequireInside); len(p) != 1 || !strings.Contains(p[0], "assertion 11 (parent-folders): no result") {
		t.Errorf("a report without assertion 11: %v", p)
	}
}

// A coordinator's folder that is visible is reported by assertion 11 and
// leaves assertion 1 alone, and an owner's by assertion 1.
func TestCoordinatorAndOwnerIdentitiesAreSeparate(t *testing.T) {
	rep, _, r := assertion11(t, func(s *Spec, f *fakeSys) {
		f.setMount("/leak", true)
		f.identities["/leak"] = coordTree
	})
	if r.Status != StatusFailed {
		t.Errorf("assertion 11: %+v", r)
	}
	if one := find(t, rep, 1); one.Status != StatusOK {
		t.Errorf("assertion 1: %+v", one)
	}
}

// The identity of a path is read once for the whole run, though assertions 1
// and 11 both compare the mount points.
func TestIdentitiesAreReadOnceAcrossAssertions(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	f.setDir(profile.CoordInside + "/feature-a")
	RunInside(s, f)
	seen := map[string]int{}
	for _, p := range f.identityRead {
		if seen[p]++; seen[p] > 1 {
			t.Errorf("the identity of %s was read twice", p)
		}
	}
	for _, p := range []string{profile.RequestsInside, profile.MailboxInside, profile.MailInside, profile.CoordInside, profile.CoordInside + "/feature-a"} {
		if seen[p] != 1 {
			t.Errorf("the identity of %s was read %d times, want once", p, seen[p])
		}
	}
}

// A name in the published folder is text the host controls: it is escaped.
func TestAssertion11DetailIsEscaped(t *testing.T) {
	rep, _, r := assertion11(t, func(s *Spec, f *fakeSys) {
		f.setDir(profile.CoordInside + "/evil\x1b[2J\nATANIQ-PROBE-REPORT {}/tree")
	})
	if r.Status != StatusFailed || !cleanDetail(r.Detail) {
		t.Errorf("%+v", r)
	}
	if line := EncodeLine(rep); strings.Count(string(line), "\n") != 1 {
		t.Errorf("the report is not one line: %q", line)
	}
}

func TestSpecCoordinatorFields(t *testing.T) {
	s := testSpec(profile.Claude)
	s.CoordinatorIdentities = []FileIdentity{{Device: 1 << 40, Inode: 1<<63 + 5}, {Device: 0, Inode: 2}}
	data, err := EncodeSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSpecBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.CoordinatorIdentities) != 2 || back.CoordinatorIdentities[0] != s.CoordinatorIdentities[0] || back.CoordinatorIdentities[1] != s.CoordinatorIdentities[1] {
		t.Errorf("round trip of the identities: %+v", back.CoordinatorIdentities)
	}
	if strings.Join(back.CoordinatorPaths, " ") != strings.Join(s.CoordinatorPaths, " ") {
		t.Errorf("round trip of the paths: %v", back.CoordinatorPaths)
	}
	// The identities are left out when empty; the paths are required.
	s.CoordinatorIdentities = nil
	data, _ = EncodeSpec(s)
	if strings.Contains(string(data), "coordinator_identities") {
		t.Error("an empty list of identities was written")
	}
	many := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("/x/%d", i))
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		mutate func(s *Spec)
		field  string
	}{
		{"no coordinator path", func(s *Spec) { s.CoordinatorPaths = nil }, "coordinator_paths"},
		{"an empty list of coordinator paths", func(s *Spec) { s.CoordinatorPaths = []string{} }, "coordinator_paths"},
		{"too many coordinator paths", func(s *Spec) { s.CoordinatorPaths = many(MaxPathEntries + 1) }, "coordinator_paths"},
		{"a relative coordinator path", func(s *Spec) { s.CoordinatorPaths[0] = "state/coords" }, "coordinator_paths[0]"},
		{"a coordinator path that is not clean", func(s *Spec) { s.CoordinatorPaths[1] = "/var/lib/../coords" }, "coordinator_paths[1]"},
		{"the root as a coordinator path", func(s *Spec) { s.CoordinatorPaths[0] = "/" }, "coordinator_paths[0]"},
		{"a control character in a coordinator path", func(s *Spec) { s.CoordinatorPaths[0] = "/var/lib/a\nb" }, "coordinator_paths[0]"},
		{"a coordinator path that is not UTF-8", func(s *Spec) { s.CoordinatorPaths[0] = "/var/lib/\xff" }, "coordinator_paths[0]"},
		{"a coordinator identity with inode 0", func(s *Spec) { s.CoordinatorIdentities = []FileIdentity{{Device: 1, Inode: 0}} }, "coordinator_identities[0]"},
		{"too many coordinator identities", func(s *Spec) {
			s.CoordinatorIdentities = make([]FileIdentity, MaxCoordinatorIdentities+1)
			for i := range s.CoordinatorIdentities {
				s.CoordinatorIdentities[i] = FileIdentity{Device: 1, Inode: uint64(i + 1)}
			}
		}, "coordinator_identities"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSpec(profile.Claude)
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if r := refusalOf(t, err); r.Field != tc.field {
				t.Errorf("field %q (%v), want %q", r.Field, err, tc.field)
			}
			if _, err := EncodeSpec(s); err == nil {
				t.Error("EncodeSpec accepted")
			}
		})
	}
	// The bounds are inclusive, and both full lists fit a spec.
	s = testSpec(profile.Claude)
	s.CoordinatorPaths = many(MaxPathEntries)
	s.CoordinatorIdentities = make([]FileIdentity, MaxCoordinatorIdentities)
	s.ForbiddenIdentities = make([]FileIdentity, MaxIdentities)
	for i := range s.CoordinatorIdentities {
		s.CoordinatorIdentities[i] = FileIdentity{Device: 1 << 40, Inode: 1<<63 + uint64(i)}
	}
	for i := range s.ForbiddenIdentities {
		s.ForbiddenIdentities[i] = FileIdentity{Device: 1 << 40, Inode: 1<<63 + uint64(i)}
	}
	if err := s.Validate(); err != nil {
		t.Errorf("lists at their bounds are refused: %v", err)
	}
	if data, err := EncodeSpec(s); err != nil || len(data) > MaxSpecBytes {
		t.Errorf("full lists do not fit the spec: %d bytes, %v", len(data), err)
	}
	// The keys of an entry are exact, as everywhere in the spec.
	good, _ := EncodeSpec(testSpec(profile.Claude))
	for name, entry := range map[string]string{
		"unknown key": `[{"device":1,"inode":2,"path":"/x"}]`,
		"key twice":   `[{"device":1,"inode":2,"inode":3}]`,
		"other case":  `[{"Device":1,"inode":2}]`,
		"negative":    `[{"device":1,"inode":-2}]`,
	} {
		in := strings.Replace(string(good), `"coordinator_identities": [`, `"coordinator_identities": `+entry+`, "x": [`, 1)
		if in == string(good) {
			t.Fatal("the spec has no coordinator_identities to replace")
		}
		if _, err := DecodeSpecBytes([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A spec of the version before, which has no coordinator path, is refused
	// for its version.
	old := strings.Replace(string(good), fmt.Sprintf(`"version": %d`, SpecVersion), `"version": 1`, 1)
	if _, err := DecodeSpecBytes([]byte(old)); err == nil || refusalOf(t, err).Field != "version" {
		t.Errorf("a version 1 spec: %v", err)
	}
}

func TestNewSpecCoordinatorPaths(t *testing.T) {
	in := profileInputs(profile.Claude)
	h := HostFacts{
		HolderPaths:    []string{"/run/example/holder.sock"},
		LoopbackTCP:    "127.0.0.1:40123",
		AbstractSocket: "example-canary-1",
		ProviderHost:   "api.example.org",
		ManagedMarker:  "example-guard-hook",
		// The first repeats a root; the second is the holder's own addition.
		CoordinatorPaths:      []string{"/srv/state/coords", "/srv/other/coords"},
		CoordinatorIdentities: []FileIdentity{{1, 10}, {1, 11}, {1, 10}},
	}
	s, err := NewSpec(in, insideEnv(t, in), h)
	if err != nil {
		t.Fatal(err)
	}
	// The two roots of the layout first, then the facts', without repeats.
	if got, want := strings.Join(s.CoordinatorPaths, " "), "/srv/state/coords /srv/state/coord-login /srv/other/coords"; got != want {
		t.Errorf("coordinator paths %q, want %q", got, want)
	}
	if l := in.Layout(); s.CoordinatorPaths[0] != l.CoordsDir() || s.CoordinatorPaths[1] != l.CoordLoginDir() {
		t.Errorf("the roots are not the layout's: %v", s.CoordinatorPaths)
	}
	if len(s.CoordinatorIdentities) != 2 || s.CoordinatorIdentities[0] != (FileIdentity{1, 10}) || s.CoordinatorIdentities[1] != (FileIdentity{1, 11}) {
		t.Errorf("identities %+v", s.CoordinatorIdentities)
	}
	if got := strings.Join(CoordinatorIdentityPaths(in, h), " "); got != strings.Join(s.CoordinatorPaths, " ") {
		t.Errorf("CoordinatorIdentityPaths %q are not the spec's coordinator paths", got)
	}
	// They are not mixed into the owner's and holder's lists, which belong to
	// assertions 1 and 6.
	for _, list := range [][]string{s.OwnerPaths, s.HolderPaths, s.MountSubstrings, IdentityPaths(in, h)} {
		for _, p := range list {
			if strings.Contains(p, "coord") {
				t.Errorf("a coordinator path in another list: %v", list)
			}
		}
	}
	// Without the facts the roots are still there, and no identity: the spec
	// is valid, and assertion 11 is then not-checked.
	h.CoordinatorPaths, h.CoordinatorIdentities = nil, nil
	s, err = NewSpec(in, insideEnv(t, in), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CoordinatorPaths) != 2 || len(s.CoordinatorIdentities) != 0 {
		t.Errorf("without facts: %v %v", s.CoordinatorPaths, s.CoordinatorIdentities)
	}
	if r := find(t, RunInside(s, goodSystem(s)), 11); r.Status != StatusNotChecked {
		t.Errorf("assertion 11 with no identity: %+v", r)
	}
	// A path of the facts that the spec refuses is refused by NewSpec.
	h.CoordinatorPaths = []string{"relative"}
	if _, err := NewSpec(in, insideEnv(t, in), h); err == nil {
		t.Error("a relative coordinator path was accepted")
	}
}

func TestCoordinatorFolders(t *testing.T) {
	in := profileInputs(profile.Claude)
	notThere := func(dir string) error { return &fs.PathError{Op: "open", Path: dir, Err: syscall.ENOENT} }
	tree := map[string][]string{
		"/srv/state/coords":             {"ataniq", "other", "stray-file"},
		"/srv/state/coords/ataniq":      {"feature-a", "feature-b"},
		"/srv/state/coords/other":       {"feature-c"},
		"/srv/state/coord-login":        {"ataniq"},
		"/srv/state/coord-login/ataniq": {"claude", "codex"},
	}
	var limits []int
	names := func(dir string, limit int) ([]string, error) {
		limits = append(limits, limit)
		if dir == "/srv/state/coords/stray-file" {
			return nil, &fs.PathError{Op: "open", Path: dir, Err: syscall.ENOTDIR}
		}
		list, ok := tree[dir]
		if !ok {
			return nil, notThere(dir)
		}
		return list, nil
	}
	got, err := CoordinatorFolders(in, names)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/srv/state/coords/ataniq/feature-a", "/srv/state/coords/ataniq/feature-a/tree", "/srv/state/coords/ataniq/feature-a/home",
		"/srv/state/coords/ataniq/feature-b", "/srv/state/coords/ataniq/feature-b/tree", "/srv/state/coords/ataniq/feature-b/home",
		"/srv/state/coords/other/feature-c", "/srv/state/coords/other/feature-c/tree", "/srv/state/coords/other/feature-c/home",
		"/srv/state/coord-login/ataniq/claude", "/srv/state/coord-login/ataniq/codex",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range limits {
		if l <= 0 || l > MaxCoordinatorIdentities {
			t.Errorf("a folder was read with the limit %d", l)
		}
	}
	// Every path is one CollectIdentities takes, and the identities name each
	// tree and home, which the roots alone do not.
	ids, err := CollectIdentities(append(CoordinatorIdentityPaths(in, HostFacts{}), got...), func(p string) (FileIdentity, error) {
		if strings.HasSuffix(p, "/feature-b/home") {
			return FileIdentity{}, notThere(p) // one that was not made yet
		}
		h := fnv.New64a()
		h.Write([]byte(p))
		return FileIdentity{Device: 3, Inode: h.Sum64() | 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The roots, /srv/state and /srv; three features with a tree and two homes;
	// two project folders under coords and one under coord-login; two logins.
	if len(ids) != 4+3+3+2+3+2 {
		t.Errorf("%d identities: %v", len(ids), ids)
	}

	// No root at all is no coordinator, and no error.
	if got, err := CoordinatorFolders(in, func(dir string, _ int) ([]string, error) { return nil, notThere(dir) }); err != nil || len(got) != 0 {
		t.Errorf("no roots: %v %v", got, err)
	}
	// A folder that cannot be read, or holds more than the limit, is an error:
	// a list cut short would make the check look better than it is.
	for name, fail := range map[string]error{
		"unreadable": syscall.EACCES,
		"too many":   fmt.Errorf("list: %w", ErrTooLarge),
		"a loop":     syscall.ELOOP,
	} {
		for _, at := range []string{"/srv/state/coords", "/srv/state/coords/ataniq", "/srv/state/coord-login", "/srv/state/coord-login/ataniq"} {
			_, err := CoordinatorFolders(in, func(dir string, limit int) ([]string, error) {
				if dir == at {
					return nil, fail
				}
				return names(dir, limit)
			})
			if err == nil || !errors.Is(err, fail) {
				t.Errorf("%s at %s: %v", name, at, err)
			}
		}
	}
	if _, err := CoordinatorFolders(in, nil); err == nil {
		t.Error("no reader was accepted")
	}
	// More folders than a spec can list identities for is refused.
	var features []string
	for i := 0; i < MaxCoordinatorIdentities/3+1; i++ {
		features = append(features, fmt.Sprintf("f%d", i))
	}
	_, err = CoordinatorFolders(in, func(dir string, _ int) ([]string, error) {
		switch dir {
		case "/srv/state/coords":
			return []string{"ataniq"}, nil
		case "/srv/state/coords/ataniq":
			return features, nil
		}
		return nil, notThere(dir)
	})
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many folders: %v", err)
	}
}
