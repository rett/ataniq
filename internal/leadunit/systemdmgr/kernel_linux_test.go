package systemdmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"ataniq/internal/systemd"
)

// treeMount points the search at a temporary tree for one test.
func treeMount(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := cgroupMount
	cgroupMount = root
	t.Cleanup(func() { cgroupMount = old })
	return root
}

func mkdirs(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindNamedFindsEveryDirectoryOfThatName(t *testing.T) {
	root := treeMount(t)
	mkdirs(t, root,
		"/m/app.slice/"+unitName,
		"/m/session.slice/"+unitName,
		"/m/app.slice/other.service",
		"/m/background.slice/deeper/than/one/"+unitName,
		"/elsewhere/"+unitName,
	)
	if err := os.WriteFile(filepath.Join(root, "m", unitName), nil, 0o644); err != nil { // a file is not a group
		t.Fatal(err)
	}
	got, err := hostKernel{}.FindNamed("/m", unitName)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"/m/app.slice/" + unitName, "/m/background.slice/deeper/than/one/" + unitName, "/m/session.slice/" + unitName}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestFindNamedNoneIsAnEmptyAnswer(t *testing.T) {
	root := treeMount(t)
	mkdirs(t, root, "/m/app.slice/x.service")
	got, err := hostKernel{}.FindNamed("/m", unitName)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

func TestFindNamedIncompleteSearchIsAnError(t *testing.T) {
	root := treeMount(t)
	if _, err := (hostKernel{}).FindNamed("/missing", unitName); err == nil {
		t.Fatal("a base that cannot be read answered")
	}
	deep := "/m"
	for i := 0; i <= findMaxDepth+1; i++ {
		deep += "/d"
	}
	mkdirs(t, root, deep)
	if _, err := (hostKernel{}).FindNamed("/m", unitName); err == nil {
		t.Fatal("a tree deeper than the bound answered")
	}
}

func TestFindNamedRefusesBadArguments(t *testing.T) {
	treeMount(t)
	for _, c := range [][2]string{{"", unitName}, {"/", unitName}, {"/m/", unitName}, {"m", ""}, {"/m", "a/b"}, {"/m", ""}, {"/m/../x", unitName}} {
		if _, err := (hostKernel{}).FindNamed(c[0], c[1]); err == nil {
			t.Errorf("%q accepted", c)
		}
	}
}

// The search stops before it queues more directories than its bound, even for
// one directory with very many children, and a tree exactly at the bound is
// still answered.
func TestFindNamedBoundsTheDirectoriesBeforeTheQueueGrows(t *testing.T) {
	old := findMaxDirs
	findMaxDirs = 5
	t.Cleanup(func() { findMaxDirs = old })
	root := treeMount(t)
	for i := 0; i < 4; i++ { // /m and four children: five directories
		mkdirs(t, root, fmt.Sprintf("/m/c%d", i))
	}
	if got, err := (hostKernel{}).FindNamed("/m", unitName); err != nil || len(got) != 0 {
		t.Fatalf("a tree at the bound: %q %v", got, err)
	}
	mkdirs(t, root, "/m/c4") // six
	if _, err := (hostKernel{}).FindNamed("/m", unitName); err == nil {
		t.Fatal("a tree over the bound answered")
	}
	// Over the bound by depth, not by one wide directory.
	root2 := treeMount(t)
	mkdirs(t, root2, "/m/a/b/c/d/e")
	if _, err := (hostKernel{}).FindNamed("/m", unitName); err == nil {
		t.Fatal("a chain over the bound answered")
	}
}

// A directory is read in chunks and nothing is lost at a chunk boundary: the
// answer is the same for every chunk size, a match is found wherever it falls
// in a chunk, and the bound still stops a directory with very many children.
func TestFindNamedReadsADirectoryInChunks(t *testing.T) {
	old := findReadChunk
	t.Cleanup(func() { findReadChunk = old })
	root := treeMount(t)
	for i := 0; i < 9; i++ {
		mkdirs(t, root, fmt.Sprintf("/m/c%d", i))
	}
	mkdirs(t, root, "/m/c3/"+unitName, "/m/c8/"+unitName, "/m/c0/x/"+unitName, "/m/"+unitName)
	for i := 0; i < 4; i++ { // files between the groups are skipped, in any chunk
		if err := os.WriteFile(filepath.Join(root, "m", fmt.Sprintf("f%d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"/m/" + unitName, "/m/c0/x/" + unitName, "/m/c3/" + unitName, "/m/c8/" + unitName}
	for _, chunk := range []int{1, 2, 3, 4, 13, 14, 256} {
		findReadChunk = chunk
		got, err := hostKernel{}.FindNamed("/m", unitName)
		if err != nil {
			t.Fatalf("chunk %d: %v", chunk, err)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk %d: %q", chunk, got)
		}
	}

	oldMax := findMaxDirs
	t.Cleanup(func() { findMaxDirs = oldMax })
	findMaxDirs = 6
	for _, chunk := range []int{1, 2, 256} {
		findReadChunk = chunk
		if _, err := (hostKernel{}).FindNamed("/m", unitName); err == nil {
			t.Fatalf("chunk %d: a directory with more children than the bound answered", chunk)
		}
	}
}

// Without the cgroup mount every path is absent, so ENOENT must not be taken
// for "the group is gone": Lookup fails and says nothing was found.
func TestLookupWithoutTheCgroupMountIsAnError(t *testing.T) {
	treeMount(t) // an empty folder: no cgroup.controllers
	b, found, err := (hostKernel{}).Lookup("/ataniq-test-no-such-group")
	if err == nil || found || b != (systemd.Boundary{}) {
		t.Fatalf("%+v %v %v", b, found, err)
	}
}

// With the mount present, a path that is not there is positively gone.
func TestLookupOfAMissingGroupWithTheMountIsGone(t *testing.T) {
	root := treeMount(t)
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b, found, err := (hostKernel{}).Lookup("/ataniq-test-no-such-group")
	if err != nil || found || b != (systemd.Boundary{}) {
		t.Fatalf("%+v %v %v", b, found, err)
	}
}
