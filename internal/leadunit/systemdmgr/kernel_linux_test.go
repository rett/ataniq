package systemdmgr

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
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
