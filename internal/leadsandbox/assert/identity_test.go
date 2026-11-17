package assert

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

var (
	ownerHome   = FileIdentity{Device: 7, Inode: 70} // an ancestor of owner paths, in testSpec
	ownerSecret = FileIdentity{Device: 7, Inode: 71} // an owner path, in testSpec
)

// assertion1 runs the inside checks on the good system after mutate and returns
// the result of assertion 1.
func assertion1(t *testing.T, mutate func(s *Spec, f *fakeSys)) Result {
	t.Helper()
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	if mutate != nil {
		mutate(&s, f)
	}
	return find(t, RunInside(s, f), 1)
}

// A bind of an owner folder whose mount root is relative to its own file
// system, as on a btrfs subvolume or a separate /home, has a root that no
// owner path matches by text. Its identity is the owner folder's.
func TestAssertion1FindsABindByIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *Spec, f *fakeSys)
		want   string
	}{
		{"a btrfs subvolume (the reviewer's probe)", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/@home/owner", Point: "/home/lead/leak", FSType: "btrfs", Source: "/dev/example1", Options: "rw", SuperOptions: "rw"})
			f.identities["/home/lead/leak"] = ownerHome
		}, `mount point "/home/lead/leak" has the identity 7:70`},
		{"a separate file system", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/owner", Point: "/home/lead/leak", FSType: "ext4", Source: "/dev/example1", Options: "rw", SuperOptions: "rw"})
			f.identities["/home/lead/leak"] = ownerHome
		}, `mount point "/home/lead/leak" has the identity 7:70`},
		{"the owner's .ssh at the lead's .ssh", func(s *Spec, f *fakeSys) {
			f.mounts = append(f.mounts, Mount{ID: 900, Root: "/@home/owner/.ssh", Point: "/home/lead/.ssh", FSType: "btrfs", Source: "/dev/example1", Options: "rw", SuperOptions: "rw"})
			f.identities["/home/lead/.ssh"] = ownerSecret
		}, "7:71"},
		{"the lead's home is the owner's home", func(s *Spec, f *fakeSys) {
			f.identities["/home/lead"] = ownerHome
		}, `the lead's home "/home/lead" has the identity 7:70`},
		{"the lead's config folder is an owner folder", func(s *Spec, f *fakeSys) {
			f.identities[profile.ConfigInside(s.Runtime)] = ownerSecret
		}, "the lead's config folder"},
		{"the lead's tree is an owner folder", func(s *Spec, f *fakeSys) { f.identities[s.Tree] = ownerHome }, "the lead's tree"},
		{"the parent of the lead's home", func(s *Spec, f *fakeSys) { f.identities["/home"] = ownerHome }, `a parent of the lead's home "/home"`},
		{"the parent of the lead's tree", func(s *Spec, f *fakeSys) { f.identities["/work"] = ownerHome }, `a parent of the lead's tree "/work"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The text checks see nothing: the substrings are not in the table
			// and no mount root, source or point is related to an owner path.
			r := assertion1(t, c.mutate)
			if r.Status != StatusFailed || !strings.Contains(r.Detail, c.want) {
				t.Errorf("%s: %+v, want failed with %q", c.name, r, c.want)
			}
		})
	}
}

// Without a forbidden identity the check cannot be made: not-checked, with the
// reason, and the start is refused.
func TestAssertion1WithNoIdentityIsNotChecked(t *testing.T) {
	for _, list := range [][]FileIdentity{nil, {}} {
		s := testSpec(profile.Claude)
		s.ForbiddenIdentities = list
		rep := RunInside(s, goodSystem(s))
		r := find(t, rep, 1)
		if r.Status != StatusNotChecked || !strings.Contains(r.Detail, "no forbidden file identity") {
			t.Errorf("an empty list: %+v", r)
		}
		if !Failed(rep, s) {
			t.Error("a report with assertion 1 not-checked allowed a start")
		}
	}
	// The path checks still fail it first.
	r := assertion1(t, func(s *Spec, f *fakeSys) {
		s.ForbiddenIdentities = nil
		f.setFile(s.HolderPaths[0], "")
	})
	if r.Status != StatusFailed || !strings.Contains(r.Detail, "could not be made") {
		t.Errorf("a failure and an empty list: %+v", r)
	}
	// Only assertion 4 may be optional, so an empty list cannot be excused.
	s := testSpec(profile.Claude)
	s.ForbiddenIdentities = nil
	s.Optional = []int{1}
	if err := s.Validate(); err == nil {
		t.Error("assertion 1 was accepted as optional")
	}
}

func TestAssertion1IdentityOkAndCounted(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	r := find(t, RunInside(s, f), 1)
	if r.Status != StatusOK || !strings.Contains(r.Detail, "none of the 2 forbidden") {
		t.Fatalf("%+v", r)
	}
	// The mount points of the table, once each, and the lead's folders and
	// their parents: /home/lead, /home, the config folder and the tree and
	// /work.
	read := map[string]bool{}
	for _, p := range f.identityRead {
		read[p] = true
	}
	for _, m := range f.mounts {
		if !read[m.Point] {
			t.Errorf("the identity of the mount point %s was not read", m.Point)
		}
	}
	for _, p := range []string{"/home/lead", "/home", profile.ConfigInside(s.Runtime), s.Tree, "/work"} {
		if !read[p] {
			t.Errorf("the identity of %s was not read", p)
		}
	}
	if read["/"] && !contains(f.mounts, "/") {
		t.Error("the root was read")
	}
	seen := map[string]int{}
	for _, p := range f.identityRead {
		if seen[p]++; seen[p] > 1 {
			t.Errorf("the identity of %s was read twice", p)
		}
	}
}

func contains(ms []Mount, point string) bool {
	for _, m := range ms {
		if m.Point == point {
			return true
		}
	}
	return false
}

func TestAssertion1IdentityQuestionsNotAsked(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(s *Spec, f *fakeSys)
		status Status
		want   string
	}{
		{"a mount point that cannot be read", func(s *Spec, f *fakeSys) { f.identityErr["/home/lead"] = syscall.EACCES }, StatusNotChecked, "identity"},
		{"a mount point that is gone is not a way in", func(s *Spec, f *fakeSys) {
			f.setMount("/gone", false)
			f.identityErr["/gone"] = &fs.PathError{Op: "lstat", Path: "/gone", Err: syscall.ENOENT}
		}, StatusOK, ""},
		{"a lead folder that is not there", func(s *Spec, f *fakeSys) {
			f.identityErr[s.Tree] = &fs.PathError{Op: "lstat", Path: s.Tree, Err: syscall.ENOENT}
			delete(f.identities, s.Tree)
			f.mounts = f.mounts[:2] // the tree is not a mount point either
		}, StatusNotChecked, "the lead's tree"},
		{"a failure beats a question not asked", func(s *Spec, f *fakeSys) {
			f.identities["/home/lead"] = ownerHome
			f.identityErr["/work"] = syscall.EACCES
		}, StatusFailed, "more checks could not be made"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := assertion1(t, c.mutate)
			if r.Status != c.status || !strings.Contains(r.Detail, c.want) {
				t.Errorf("%+v, want %s with %q", r, c.status, c.want)
			}
		})
	}
}

// The identity of the mounts is read for a bounded number of them.
func TestAssertion1IdentityIsBoundedByMounts(t *testing.T) {
	s := testSpec(profile.Claude)
	f := goodSystem(s)
	for i := 0; i < maxIdentityMounts+10; i++ {
		f.setMount(fmt.Sprintf("/m/%d", i), true)
	}
	r := find(t, RunInside(s, f), 1)
	if r.Status != StatusNotChecked || !strings.Contains(r.Detail, "was not read") {
		t.Errorf("%+v", r)
	}
	read := map[string]bool{}
	for _, p := range f.identityRead {
		read[p] = true
	}
	last := fmt.Sprintf("/m/%d", maxIdentityMounts+9)
	if read[last] {
		t.Errorf("the identity of %s, past the bound, was read", last)
	}
	if !read["/m/0"] {
		t.Error("the identity of the first mounts was not read")
	}
	if len(f.identityRead) > maxIdentityMounts+10 {
		t.Errorf("%d identities were read, want at most %d for mounts and a few folders", len(f.identityRead), maxIdentityMounts)
	}
}

func TestSpecForbiddenIdentities(t *testing.T) {
	s := testSpec(profile.Claude)
	s.ForbiddenIdentities = []FileIdentity{{Device: 1 << 40, Inode: 1<<63 + 5}, {Device: 0, Inode: 2}}
	data, err := EncodeSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeSpecBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.ForbiddenIdentities) != 2 || back.ForbiddenIdentities[0] != s.ForbiddenIdentities[0] || back.ForbiddenIdentities[1] != s.ForbiddenIdentities[1] {
		t.Errorf("round trip: %+v", back.ForbiddenIdentities)
	}
	// The list is left out when empty, and then decodes as empty.
	s.ForbiddenIdentities = nil
	data, _ = EncodeSpec(s)
	if strings.Contains(string(data), "forbidden_identities") {
		t.Error("an empty list was written")
	}
	// Refusals: inode 0, too many, an unknown key or a key twice in an entry.
	s.ForbiddenIdentities = []FileIdentity{{Device: 1, Inode: 0}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden_identities[0]") {
		t.Errorf("inode 0: %v", err)
	}
	s.ForbiddenIdentities = make([]FileIdentity, MaxIdentities+1)
	for i := range s.ForbiddenIdentities {
		s.ForbiddenIdentities[i] = FileIdentity{Device: 1, Inode: uint64(i + 1)}
	}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden_identities") {
		t.Errorf("too many: %v", err)
	}
	s.ForbiddenIdentities = s.ForbiddenIdentities[:MaxIdentities]
	if data, err := EncodeSpec(s); err != nil || len(data) > MaxSpecBytes {
		t.Errorf("a full list does not fit the spec: %d bytes, %v", len(data), err)
	}
	good, _ := EncodeSpec(testSpec(profile.Claude))
	for name, entry := range map[string]string{
		"unknown key": `[{"device":1,"inode":2,"path":"/x"}]`,
		"key twice":   `[{"device":1,"inode":2,"inode":3}]`,
		"other case":  `[{"Device":1,"inode":2}]`,
		"negative":    `[{"device":1,"inode":-2}]`,
		"a fraction":  `[{"device":1,"inode":2.5}]`,
		"a string":    `[{"device":"1","inode":2}]`,
	} {
		in := strings.Replace(string(good), `"forbidden_identities": [`, `"forbidden_identities": `+entry+`, "x": [`, 1)
		if _, err := DecodeSpecBytes([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewSpecCarriesIdentities(t *testing.T) {
	in := profileInputs(profile.Claude)
	h := HostFacts{
		HolderPaths: []string{"/run/example/holder.sock"}, OwnerPaths: []string{"/home/owner/extra"},
		MountSubstrings: []string{"/run/example"},
		LoopbackTCP:     "127.0.0.1:40123", AbstractSocket: "example-canary-1", ProviderHost: "api.example.org",
		ManagedMarker: "example-guard-hook",
		Identities:    []FileIdentity{{Device: 7, Inode: 70}, {Device: 7, Inode: 71}, {Device: 7, Inode: 70}},
	}
	s, err := NewSpec(in, insideEnv(t, in), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ForbiddenIdentities) != 2 || s.ForbiddenIdentities[0] != (FileIdentity{7, 70}) || s.ForbiddenIdentities[1] != (FileIdentity{7, 71}) {
		t.Errorf("identities: %+v", s.ForbiddenIdentities)
	}
	// Without identities the spec is made, and assertion 1 is not-checked.
	h.Identities = nil
	s, err = NewSpec(in, insideEnv(t, in), h)
	if err != nil || len(s.ForbiddenIdentities) != 0 {
		t.Errorf("no identities: %v %+v", err, s.ForbiddenIdentities)
	}
	// The paths to collect are the spec's owner and holder paths.
	got := IdentityPaths(in, h)
	want := map[string]bool{}
	for _, p := range append(append([]string(nil), s.OwnerPaths...), s.HolderPaths...) {
		want[p] = true
	}
	if len(got) != len(want) {
		t.Errorf("IdentityPaths = %v, want the %d owner and holder paths of the spec", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("IdentityPaths has %s", p)
		}
	}
}

func fakeStat(ids map[string]FileIdentity, errs map[string]error) func(string) (FileIdentity, error) {
	return func(p string) (FileIdentity, error) {
		if err := errs[p]; err != nil {
			return FileIdentity{}, err
		}
		if id, ok := ids[p]; ok {
			return id, nil
		}
		return FileIdentity{}, &fs.PathError{Op: "lstat", Path: p, Err: syscall.ENOENT}
	}
}

func TestCollectIdentities(t *testing.T) {
	ids := map[string]FileIdentity{
		"/home":                     {1, 10},
		"/home/owner":               {1, 11},
		"/home/owner/.ssh":          {1, 12},
		"/home/owner/work":          {1, 13},
		"/home/owner/work/ataniq":   {1, 14},
		"/run":                      {2, 20},
		"/run/example":              {2, 21},
		"/run/example/holder.sock":  {2, 22},
		"/":                         {1, 2},
		"/home/owner/.ssh/same":     {1, 12},
		"/home/owner/not-a-problem": {1, 99},
	}
	paths := []string{"/home/owner/.ssh", "/home/owner/work/ataniq", "/home/owner/.codex", "/run/example/holder.sock", "/var/lib/absent/x"}
	got, err := CollectIdentities(paths, fakeStat(ids, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Each path that exists, then its ancestors, never the root; a path that
	// does not exist is skipped, and so is the ancestor that does not exist;
	// the ancestors that do exist (/home/owner of .codex) are kept; no repeats.
	want := []FileIdentity{{1, 12}, {1, 11}, {1, 10}, {1, 14}, {1, 13}, {2, 22}, {2, 21}, {2, 20}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	for _, id := range got {
		if id == (FileIdentity{1, 2}) {
			t.Error("the root was collected")
		}
	}
	// Another stat error is an error, not a shorter list.
	_, err = CollectIdentities(paths, fakeStat(ids, map[string]error{"/home/owner": &fs.PathError{Op: "lstat", Path: "/home/owner", Err: syscall.EACCES}}))
	if err == nil || !errors.Is(err, syscall.EACCES) {
		t.Errorf("a stat error: %v", err)
	}
	for _, bad := range []string{"relative/x", "/a/../b", "/a//b", "/", "", "/a/\x00b", "/a/"} {
		if _, err := CollectIdentities([]string{bad}, fakeStat(ids, nil)); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if _, ok := err.(*Refusal); !ok {
			t.Errorf("%q: %T", bad, err)
		}
	}
	if _, err := CollectIdentities(paths, nil); err == nil {
		t.Error("no stat")
	}
	if got, err := CollectIdentities(nil, fakeStat(ids, nil)); err != nil || len(got) != 0 {
		t.Errorf("no paths: %v %v", got, err)
	}
	// A list that would be cut is refused, not cut.
	many := map[string]FileIdentity{}
	var ps []string
	for i := 0; i < MaxIdentities+1; i++ {
		p := fmt.Sprintf("/d/%d", i)
		many[p] = FileIdentity{3, uint64(i + 1)}
		ps = append(ps, p)
	}
	if _, err := CollectIdentities(ps, fakeStat(many, nil)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("an over-long list: %v", err)
	}
}
