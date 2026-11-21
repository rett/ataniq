//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadwire"
)

// A lead whose config says "record_screens": true is started with
// --record-dir <its runtime folder>/record, and the other lead without it.
// The daemon does not make, read or name the folder: it is in no receipt, no
// evidence line, no table line and no log line.
func TestLeadRecordScreensPassesTheFlagOnlyForThatLead(t *testing.T) {
	l := newLeadEnv(t)
	l.w.Config.Entries[0].RecordScreens = true // alpha records, beta does not
	l.resetScratch()
	l.start()
	l.up("alpha", submitted)
	l.up("beta", submitted)

	specs := l.fake.StartedSpecs()
	if len(specs) != 2 {
		t.Fatalf("%d units started", len(specs))
	}
	record := filepath.Join(l.holderDir("alpha"), "record")
	found := map[string]bool{}
	for _, spec := range specs {
		n := 0
		for _, a := range spec.Argv {
			if a == "--record-dir" {
				n++
			}
		}
		switch {
		case argHas(spec.Argv, "--role", roleOf("alpha")):
			found["alpha"] = true
			if n != 1 || !argHas(spec.Argv, "--record-dir", record) {
				t.Errorf("alpha's holder is not given its record folder once: %q", spec.Argv)
			}
		case argHas(spec.Argv, "--role", roleOf("beta")):
			found["beta"] = true
			if n != 0 {
				t.Errorf("beta's holder is given --record-dir: %q", spec.Argv)
			}
		default:
			t.Errorf("a unit of no lead: %q", spec.Argv)
		}
	}
	if !found["alpha"] || !found["beta"] {
		t.Fatalf("started %v", found)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Errorf("the daemon made the record folder: %v", err)
	}
	for what, text := range map[string]string{
		"the command table": string(l.tableBytes()),
		"the log":           l.log.String(),
		"alpha's evidence":  readOrEmpty(t, l.cfgOf("alpha").EvidencePath()),
		"the lead list":     strings.Join(listRaw(l), ""),
	} {
		if strings.Contains(text, record) || strings.Contains(text, "record_screens") || strings.Contains(text, "--record-dir") {
			t.Errorf("%s names the recording: %s", what, text)
		}
	}
}

// A start of a recording lead whose record folder is still there (from an
// earlier recording, or a link at its name) is refused by the daemon before
// anything changes, with a fixed text that names no path and the code of a
// refused preflight, which the console has words for; the folder is left as
// it is. Once it is gone the start goes on. A lead that does not record is
// not held up by a folder of that name.
func TestLeadStartRefusedWhileTheRecordFolderIsThere(t *testing.T) {
	l := newLeadEnv(t)
	l.w.Config.Entries[0].RecordScreens = true // alpha records, beta does not
	l.resetScratch()
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	record := filepath.Join(l.holderDir("alpha"), "record")
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record, "0001.txt"), []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedNow := func(what string) {
		t.Helper()
		r := l.startLead("alpha")
		want(t, r, leadwire.StatusRefused, codePreflight)
		if r.Message != msgRecordLeft {
			t.Errorf("%s: the refusal says %q", what, r.Message)
		}
		if len(l.fake.StartedSpecs()) != 0 {
			t.Fatalf("%s: a unit was started", what)
		}
		if row := l.row("alpha"); row.Epoch != 0 {
			t.Fatalf("%s: the registry changed: %+v", what, row)
		}
	}
	refusedNow("an earlier recording")
	if b, err := os.ReadFile(filepath.Join(record, "0001.txt")); err != nil || string(b) != "earlier\n" {
		t.Fatalf("the earlier recording was changed: %q %v", b, err)
	}
	if err := os.RemoveAll(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), record); err != nil {
		t.Fatal(err)
	}
	refusedNow("a link")
	if strings.Contains(l.log.String(), record) || !strings.Contains(l.log.String(), logRecordLeft) {
		t.Errorf("the log names the folder, or not the refusal: %s", l.log.String())
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")

	want(t, l.define("beta"), leadwire.StatusDone, "")
	l.holder("beta", submitted)
	if err := os.Mkdir(filepath.Join(l.holderDir("beta"), "record"), 0o700); err != nil {
		t.Fatal(err)
	}
	want(t, l.startLead("beta"), leadwire.StatusDone, "")
}

func readOrEmpty(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// listRaw is the rows of lead.list as the client sees them.
func listRaw(l *leadEnv) []string {
	var out []string
	for _, r := range l.list().Leads {
		out = append(out, r.Key, r.Role, r.State, r.Unit)
	}
	return out
}
