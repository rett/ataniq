//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
