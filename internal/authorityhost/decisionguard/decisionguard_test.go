package decisionguard

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGuardFileIsTheSwitchInhibit(t *testing.T) {
	if GuardFile != switcher.InhibitFile {
		t.Fatalf("GuardFile %q differs from switcher.InhibitFile %q", GuardFile, switcher.InhibitFile)
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the state folder to check.
		setup func(t *testing.T, root string) string
		code  string // "" allows
	}{
		{"clean folder allows", func(t *testing.T, root string) string {
			return root
		}, ""},
		{"other files do not matter", func(t *testing.T, root string) string {
			mustWrite(t, filepath.Join(root, "txn.json"), "{}")
			mustWrite(t, filepath.Join(root, "decisions.inhibit.bak"), "x")
			return root
		}, ""},
		{"guard file refuses", func(t *testing.T, root string) string {
			mustWrite(t, filepath.Join(root, GuardFile), "{}")
			return root
		}, CodeInhibited},
		{"empty guard file refuses", func(t *testing.T, root string) string {
			mustWrite(t, filepath.Join(root, GuardFile), "")
			return root
		}, CodeInhibited},
		{"guard symlink to nothing refuses", func(t *testing.T, root string) string {
			if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, GuardFile)); err != nil {
				t.Fatal(err)
			}
			return root
		}, CodeInhibited},
		{"guard symlink to a file refuses", func(t *testing.T, root string) string {
			mustWrite(t, filepath.Join(root, "other"), "x")
			if err := os.Symlink("other", filepath.Join(root, GuardFile)); err != nil {
				t.Fatal(err)
			}
			return root
		}, CodeInhibited},
		{"guard directory refuses", func(t *testing.T, root string) string {
			if err := os.Mkdir(filepath.Join(root, GuardFile), 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}, CodeInhibited},
		{"guard socket refuses", func(t *testing.T, root string) string {
			l, err := net.Listen("unix", filepath.Join(root, GuardFile))
			if err != nil {
				t.Skipf("no unix socket here: %v", err)
			}
			t.Cleanup(func() { l.Close() })
			return root
		}, CodeInhibited},
		{"state folder missing refuses", func(t *testing.T, root string) string {
			return filepath.Join(root, "gone")
		}, CodeStateDir},
		{"state folder name empty refuses", func(t *testing.T, root string) string {
			return ""
		}, CodeStateDir},
		{"state folder is a file refuses", func(t *testing.T, root string) string {
			p := filepath.Join(root, "file")
			mustWrite(t, p, "x")
			return p
		}, CodeStateDir},
		{"state folder is a symlink to a good folder refuses", func(t *testing.T, root string) string {
			// The switch's folders are root-owned real folders; a link in
			// their place is not the switch's, so it does not count as
			// "no guard".
			good := filepath.Join(root, "good")
			if err := os.Mkdir(good, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(good, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, CodeStateDir},
		{"state folder is a dangling symlink refuses", func(t *testing.T, root string) string {
			link := filepath.Join(root, "link")
			if err := os.Symlink(filepath.Join(root, "gone"), link); err != nil {
				t.Fatal(err)
			}
			return link
		}, CodeStateDir},
		{"unreadable folder refuses", func(t *testing.T, root string) string {
			if os.Geteuid() == 0 {
				t.Skip("root reads a mode 0 folder")
			}
			d := filepath.Join(root, "locked")
			if err := os.Mkdir(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(d, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(d, 0o755) })
			return d
		}, CodeUnreadable},
		{"folder under an unsearchable parent refuses", func(t *testing.T, root string) string {
			if os.Geteuid() == 0 {
				t.Skip("root searches a mode 0 folder")
			}
			parent := filepath.Join(root, "parent")
			if err := os.MkdirAll(filepath.Join(parent, "state"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(parent, 0o755) })
			return filepath.Join(parent, "state")
		}, CodeStateDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.setup(t, t.TempDir())
			err := Check(dir)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("Check(%q) = %v, want nil", dir, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check(%q) = nil, want %s", dir, tc.code)
			}
			var ge *Error
			if !errors.As(err, &ge) {
				t.Fatalf("Check error %T is not *Error", err)
			}
			if ge.Code != tc.code {
				t.Fatalf("Code = %q, want %q", ge.Code, tc.code)
			}
			if ge.Dir != dir {
				t.Fatalf("Dir = %q, want %q", ge.Dir, dir)
			}
			want := tc.code + ": " + dir
			if err.Error() != want {
				t.Fatalf("Error() = %q, want %q", err.Error(), want)
			}
		})
	}
}

// The error text is the code and the caller's folder only: nothing from the
// operating system (no "no such file", no "permission denied", no errno).
func TestErrorTextHasNoOSText(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "gone")
	inhibited := filepath.Join(root, "inhibited")
	if err := os.Mkdir(inhibited, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(inhibited, GuardFile), "{}")
	for _, dir := range []string{missing, inhibited, root + "/file/x"} {
		err := Check(dir)
		if err == nil {
			t.Fatalf("Check(%q) allowed", dir)
		}
		text := strings.ToLower(err.Error())
		for _, bad := range []string{"no such file", "permission denied", "not a directory", "lstat", "errno", "syscall", "operation not permitted"} {
			if strings.Contains(text, bad) {
				t.Fatalf("error %q contains OS text %q", err, bad)
			}
		}
		var ge *Error
		if errors.As(err, &ge) && errors.Unwrap(err) != nil {
			t.Fatalf("error wraps %v", errors.Unwrap(err))
		}
	}
}

// Check holds no state: the guard appearing and going between two calls is
// seen by the second call.
func TestCheckSeesEveryChange(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(dir, GuardFile)
	if err := Check(dir); err != nil {
		t.Fatalf("first: %v", err)
	}
	mustWrite(t, guard, "{}")
	if err := Check(dir); err == nil {
		t.Fatal("allowed with the guard there")
	}
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	if err := Check(dir); err != nil {
		t.Fatalf("after removal: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := Check(dir); err == nil {
		t.Fatal("allowed with the state folder gone")
	}
}

// TestCheckFailsClosedWhenTheFolderGoesBetweenLookups: Check looks the
// state folder up, then the guard file in it. "No such file" for the
// guard allows only when it is the guard that is missing from that same
// folder: a folder removed, replaced or swapped for a link between the two
// lookups makes the second answer "no such file" too, and refuses.
func TestCheckFailsClosedWhenTheFolderGoesBetweenLookups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"removed", func(t *testing.T, dir string) {
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced by another folder", func(t *testing.T, dir string) {
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced by a link to another folder", func(t *testing.T, dir string) {
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir+".other", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+".other", dir); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			betweenLookups = func() { tc.change(t, dir) }
			t.Cleanup(func() { betweenLookups = nil })
			err := Check(dir)
			var ge *Error
			if !errors.As(err, &ge) || ge.Code != CodeStateDir {
				t.Fatalf("Check = %v, want %s", err, CodeStateDir)
			}
		})
	}
	t.Run("unchanged", func(t *testing.T) {
		dir := t.TempDir()
		ran := false
		betweenLookups = func() { ran = true }
		t.Cleanup(func() { betweenLookups = nil })
		if err := Check(dir); err != nil || !ran {
			t.Fatalf("Check = %v, hook ran %v", err, ran)
		}
	})
}
