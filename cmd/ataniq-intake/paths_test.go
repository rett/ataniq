package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerUIDReadsTheOwner(t *testing.T) {
	fi, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := ownerUID(fi)
	if !ok || int(uid) != os.Geteuid() {
		t.Fatalf("owner %d (%v), want %d", uid, ok, os.Geteuid())
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		parent, child string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b/c/d", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/x", false},
		{"/", "/a", true},
	} {
		if got := within(c.parent, c.child); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestAcceptArgIsOneShellWord(t *testing.T) {
	for in, want := range map[string]string{
		".claude/settings.json": `--accept-path '.claude/settings.json'`,
		"has space":             `--accept-path 'has space'`,
		"it's":                  `--accept-path 'it'\''s'`,
		"a\nb":                  `--accept-path 'a\nb'`,
		`back\slash`:            `--accept-path 'back\\slash'`,
	} {
		if got := acceptArg(in); got != want {
			t.Errorf("acceptArg(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestRecordsAndWorkMustBeSeparate: the sandbox of a verify works in the work
// folder, so the records folder must not be the work folder or inside it, and
// the work folder must not be inside the records folder. Names are compared
// as given and with their symlinks resolved. Nothing runs.
func TestRecordsAndWorkMustBeSeparate(t *testing.T) {
	w := newWorld(t)
	fr := &fakeRunner{}
	w.runner = fr
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)
	mk := func(p string) string {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	nestedRecords := mk(filepath.Join(w.work, "rec"))
	nestedWork := mk(filepath.Join(w.records, "work"))
	workLink := filepath.Join(w.other, "work-link")
	if err := os.Symlink(w.work, workLink); err != nil {
		t.Fatal(err)
	}
	recordsLink := filepath.Join(w.other, "records-link")
	if err := os.Symlink(w.records, recordsLink); err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]string{
		"equal":                        {w.work, w.work},
		"records inside work":          {nestedRecords, w.work},
		"work inside records":          {w.records, nestedWork},
		"records inside work, by link": {filepath.Join(workLink, "rec"), w.work},
		"work inside records, by link": {w.records, filepath.Join(recordsLink, "work")},
		"equal, by a link":             {filepath.Join(workLink, "rec"), nestedRecords},
	}
	for name, c := range cases {
		records, work := c[0], c[1]
		// Each case passes the checks of the single folders: only the layout
		// is wrong.
		if err := owned("records", records, w.state); err != nil {
			t.Fatalf("%s: records: %v", name, err)
		}
		if err := owned("work", work, w.state); err != nil {
			t.Fatalf("%s: work: %v", name, err)
		}
		code, out, errb := w.sub("verify", "--role", testRole, "--branch", "main", "--gate", "a=true",
			"--sandbox", w.template(), "--records", records, "--work", work)
		if code != exitUsage || out != "" || !strings.Contains(errb, "separate folders") {
			t.Errorf("%s: exit %d\n%s\n%s", name, code, out, errb)
		}
	}
	if fr.calls != 0 || len(w.planner.VerifyRequests()) != 0 {
		t.Error("a refused layout still started a run")
	}
	// Two separate folders still work.
	code, out, errb := w.sub("verify", w.verifyArgs(testRole, "main", "true")...)
	if code != exitOK && code != exitRefused {
		t.Errorf("separate folders: exit %d\n%s\n%s", code, out, errb)
	}
	if fr.calls != 1 {
		t.Errorf("separate folders: %d runs, want 1", fr.calls)
	}
}
