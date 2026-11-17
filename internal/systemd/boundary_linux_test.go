package systemd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree makes a temporary cgroup tree and points the package at it.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
	return root
}

func group(t *testing.T, root, path, events string) {
	t.Helper()
	dir := filepath.Join(root, path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if events != "" {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte(events), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBoundaryOfIdentity(t *testing.T) {
	root := tree(t)
	group(t, root, "/user.slice/a.service", "")
	b, err := BoundaryOf("/user.slice/a.service")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "user.slice/a.service"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Path != "/user.slice/a.service" || b.Identity != IdentityOf(fi) || b.Identity.Inode == 0 || b.Identity.UID != uint32(os.Getuid()) {
		t.Fatalf("%+v", b)
	}
	again, err := BoundaryOf(b.Path)
	if err != nil || again != b {
		t.Fatal("identity not stable", again, err)
	}
	other := filepath.Join(root, "user.slice/b.service")
	group(t, root, "/user.slice/b.service", "")
	if o, err := BoundaryOf("/user.slice/b.service"); err != nil || o.Identity == b.Identity {
		t.Fatalf("two directories share an identity: %+v %v (%s)", o, err, other)
	}
}

func TestBoundaryOfRefusals(t *testing.T) {
	root := tree(t)
	group(t, root, "/real", "")
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"empty":             "",
		"relative":          "real",
		"root":              "/",
		"unclean dots":      "/real/../real",
		"unclean slash":     "/real/",
		"double slash":      "//real",
		"newline":           "/real\n",
		"carriage return":   "/real\r",
		"nul":               "/real\x00",
		"space":             "/re al",
		"percent":           "/re%al",
		"quote":             "/re'al",
		"too long":          "/" + strings.Repeat("x", 600),
		"missing":           "/missing",
		"regular file":      "/file",
		"final symlink":     "/link",
		"component symlink": "/link/..",
	} {
		if b, err := BoundaryOf(path); err == nil {
			t.Errorf("%s: accepted %+v", name, b)
		}
	}
	if _, err := BoundaryOf("/link"); err == nil {
		t.Fatal("symlink followed")
	}
}

func TestPopulated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events string
		want   bool
		err    string
	}{
		{"populated", "populated 1\nfrozen 0\n", true, ""},
		{"empty", "populated 0\nfrozen 0\n", false, ""},
		{"populated last", "frozen 0\npopulated 1\n", true, ""},
		{"only populated", "populated 0", false, ""},
		{"no populated line", "frozen 0\n", false, "unproved boundary population"},
		{"duplicate", "populated 0\npopulated 0\n", false, "invalid populated value"},
		{"duplicate differing", "populated 1\npopulated 0\n", false, "invalid populated value"},
		{"bad value", "populated 2\n", false, "invalid populated value"},
		{"value text", "populated yes\n", false, "invalid populated value"},
		{"one field", "populated\n", false, "invalid cgroup events"},
		{"three fields", "populated 1 x\n", false, "invalid cgroup events"},
		{"blank line", "populated 1\n\nfrozen 0\n", false, "invalid cgroup events"},
		{"empty file", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t)
			group(t, root, "/g", tc.events)
			if tc.events == "" {
				if err := os.WriteFile(filepath.Join(root, "g/cgroup.events"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				tc.err = "invalid cgroup events"
			}
			b, err := BoundaryOf("/g")
			if err != nil {
				t.Fatal(err)
			}
			got, err := Populated(b)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err || got {
					t.Fatalf("got %v, %v; want %s", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestPopulatedIdentity(t *testing.T) {
	root := tree(t)
	group(t, root, "/g", "populated 0\n")
	b, err := BoundaryOf("/g")
	if err != nil {
		t.Fatal(err)
	}
	// A forged identity, as from a record of another directory.
	forged := b
	forged.Identity.Inode++
	if got, err := Populated(forged); err == nil || got || err.Error() != "cgroup identity missing or changed" {
		t.Fatalf("forged inode: %v %v", got, err)
	}
	forged = b
	forged.Identity.Device++
	if _, err := Populated(forged); err == nil {
		t.Fatal("forged device accepted")
	}
	forged = b
	forged.Identity.UID++
	if _, err := Populated(forged); err == nil {
		t.Fatal("forged owner accepted")
	}
	// The directory was replaced: the same path now names another object.
	if err := os.Rename(filepath.Join(root, "g"), filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	group(t, root, "/g", "populated 0\n")
	if got, err := Populated(b); err == nil || got {
		t.Fatalf("replaced directory: %v %v", got, err)
	}
	// And the directory is gone.
	if err := os.RemoveAll(filepath.Join(root, "g")); err != nil {
		t.Fatal(err)
	}
	if got, err := Populated(b); err == nil || got {
		t.Fatalf("missing directory: %v %v", got, err)
	}
	// A path that is not a valid boundary never reads events.
	if _, err := Populated(Boundary{Path: "/", Identity: b.Identity}); err == nil {
		t.Fatal("root accepted")
	}
}

func TestPopulatedEventsRead(t *testing.T) {
	root := tree(t)
	group(t, root, "/g", "")
	b, err := BoundaryOf("/g")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Populated(b); !os.IsNotExist(err) {
		t.Fatalf("missing events: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "g/cgroup.events"), []byte("populated 0\n"+strings.Repeat("x 1\n", 2000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Populated(b); err == nil || got {
		t.Fatalf("excessive events accepted: %v %v", got, err)
	}
}

func TestBoundaryJSONShape(t *testing.T) {
	b := Boundary{Path: "/a", Identity: FileIdentity{Device: 1, Inode: 2, UID: 3}}
	out, err := json.Marshal(b)
	if err != nil || string(out) != `{"path":"/a","identity":{"device":1,"inode":2,"uid":3}}` {
		t.Fatalf("%s %v", out, err)
	}
	var back Boundary
	if err := json.Unmarshal(out, &back); err != nil || back != b {
		t.Fatal(back, err)
	}
}
