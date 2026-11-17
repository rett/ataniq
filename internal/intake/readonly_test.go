package intake

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshot describes the whole repository folder: every file with its size
// and modification time, the refs and the object counts.
func snapshot(t *testing.T, s *scene) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(s.bare, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.bare, p)
		fmt.Fprintf(&b, "%s %v %d %d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString(s.b("for-each-ref") + "\n")
	b.WriteString(s.b("count-objects", "-v") + "\n")
	return b.String()
}

// assertUntouched fails when the repository changed since before was taken,
// or when an index file or a work tree appeared.
func assertUntouched(t *testing.T, s *scene, before string) {
	t.Helper()
	if after := snapshot(t, s); after != before {
		t.Errorf("the repository changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, name := range []string{"index", "index.lock", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD"} {
		if _, err := os.Lstat(filepath.Join(s.bare, name)); err == nil {
			t.Errorf("%s appeared in the repository", name)
		}
	}
	entries, err := os.ReadDir(s.bare)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "HEAD", "config", "description", "hooks", "info", "objects", "refs", "branches", "packed-refs":
		default:
			t.Errorf("unexpected entry %q in the repository", e.Name())
		}
	}
}

func TestInspectWritesNothing(t *testing.T) {
	s, _ := plainScene(t)
	before := snapshot(t, s)
	s.inspect()
	assertUntouched(t, s, before)
}
