//go:build linux && amd64

package harness

import (
	"os"
	"strings"
	"testing"
)

func TestParseBwrapVersion(t *testing.T) {
	for line, want := range map[string][3]int{
		"bubblewrap 0.10.0":         {0, 10, 0},
		"bubblewrap 0.11.0":         {0, 11, 0},
		"bubblewrap 1.2":            {1, 2, 0},
		"bubblewrap 0.9.0-rc1":      {0, 9, 0},
		"bubblewrap 0.8.0 (distro)": {0, 8, 0},
		"bubblewrap 12.34.56":       {12, 34, 56},
	} {
		if got, ok := parseBwrapVersion(line); !ok || got != want {
			t.Errorf("%q: %v, %v; want %v", line, got, ok, want)
		}
	}
	for _, line := range []string{
		"", "bubblewrap", "bubblewrap ", "bubblewrap x", "bubblewrap 11", "bubblewrap 0.", "bubblewrap .10.0", "bubblewrap 0..1",
		"bubblewrap 0.10.0.1", "bubblewrap 0.1000000.0", "bwrap 0.10.0", "0.10.0", " bubblewrap 0.10.0", "Bubblewrap 0.10.0", "bubblewrap  0.10.0",
		"bubblewrap -1.10.0",
	} {
		if got, ok := parseBwrapVersion(line); ok {
			t.Errorf("%q was read as %v", line, got)
		}
	}
	// The constant itself is a release.
	if _, ok := parseBwrapVersion("bubblewrap " + MinBwrapBindFD); !ok {
		t.Errorf("the minimum %q is not a release", MinBwrapBindFD)
	}
}

func TestCheckBwrapBindFD(t *testing.T) {
	for _, c := range []struct {
		line          string
		refused, note bool
	}{
		{"bubblewrap 0.9.9", true, false},
		{"bubblewrap 0.9.100", true, false},
		{"bubblewrap 0.0.1", true, false},
		{"bubblewrap 0.10.0", false, false},
		{"bubblewrap 0.10.1", false, false},
		{"bubblewrap 0.100.0", false, false},
		{"bubblewrap 1.0.0", false, false},
		{"bubblewrap 1.9", false, false},
		{"", false, true},
		{"bubblewrap is here", false, true},
	} {
		note, err := checkBwrapBindFD(c.line)
		if (err != nil) != c.refused || (note != "") != c.note {
			t.Errorf("%q: note %q, error %v; want refused %v, a note %v", c.line, note, err, c.refused, c.note)
		}
		if err != nil && (!strings.Contains(err.Error(), "--bind-fd") || !strings.Contains(err.Error(), MinBwrapBindFD)) {
			t.Errorf("%q: the refusal %q does not name the option and the release", c.line, err)
		}
		if note != "" && (!strings.Contains(note, "not known") || !strings.Contains(note, "assumed")) {
			t.Errorf("%q: the note %q", c.line, note)
		}
	}
}

func TestOnlyEntry(t *testing.T) {
	dir := t.TempDir()
	if err := onlyEntry("field", dir, "config"); err == nil || !strings.Contains(err.Error(), "does not hold the file config") {
		t.Errorf("an empty folder: %v", err)
	}
	if err := os.WriteFile(dir+"/config", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlyEntry("field", dir, "config"); err != nil {
		t.Errorf("the file alone: %v", err)
	}
	// Many entries: the folder is refused after a bounded read, and the
	// reason names one of them.
	for i := 0; i < 3*maxFolderEntries; i++ {
		if err := os.WriteFile(dir+"/extra-"+strings.Repeat("x", i+1), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := onlyEntry("field", dir, "config")
	if err == nil || !strings.Contains(err.Error(), "besides config") || strings.Count(err.Error(), "extra-") != 1 {
		t.Errorf("a folder with many entries: %v", err)
	}
	// A folder that is a link is not followed.
	link := t.TempDir() + "/link"
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := onlyEntry("field", link, "config"); err == nil || !strings.Contains(err.Error(), "cannot open the folder") {
		t.Errorf("a link in place of the folder: %v", err)
	}
	// A long name is clipped in the reason.
	long := t.TempDir()
	if err := os.WriteFile(long+"/"+strings.Repeat("n", 200), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := onlyEntry("field", long, "config"); err == nil || len(err.Error()) > 400 {
		t.Errorf("a long name: %v", err)
	}
}
