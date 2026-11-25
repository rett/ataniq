//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadwire"
)

// testHolderBuild is the build the tests' daemon expects; otherHolderBuild is
// a build of another revision.
var (
	testHolderBuild = buildid.Identity{Module: "ataniq", Version: "v0.0.14-0.20261124010203-222222222222",
		Revision: "2222222222222222222222222222222222222222"}
	otherHolderBuild = buildid.Identity{Module: "ataniq", Version: "v0.0.13-0.20261120010203-111111111111",
		Revision: "1111111111111111111111111111111111111111"}
)

// sameHolderBuild is a check of the holder's build that finds every holder to
// be the expected build: the tests' holders are fakes and their executable
// does not exist.
func sameHolderBuild() *buildid.Checker {
	return &buildid.Checker{
		Expected:   testHolderBuild,
		Executable: func(string) (buildid.Identity, error) { return testHolderBuild, nil },
		Record: func(string) (buildid.Record, error) {
			b := testHolderBuild
			return buildid.Record{Build: &b}, nil
		},
	}
}

// TestHolderVersionSkewCodeIsBuildidReason: the receipt's code is the name
// the build check gives the refusal.
func TestHolderVersionSkewCodeIsBuildidReason(t *testing.T) {
	if codeHolderVersionSkew != buildid.Reason {
		t.Fatalf("%q is not %q", codeHolderVersionSkew, buildid.Reason)
	}
}

// TestCanaryStartRefusesAHolderOfAnotherBuild: the holder executable of the
// config is read before the start, and one of another build is refused by
// name, naming both builds, with nothing changed: the role stays stopped at
// epoch 0.
func TestCanaryStartRefusesAHolderOfAnotherBuild(t *testing.T) {
	c := newCanaryEnv(t)
	var asked []string
	c.build = sameHolderBuild()
	c.build.Executable = func(p string) (buildid.Identity, error) {
		asked = append(asked, p)
		return otherHolderBuild, nil
	}
	c.start()
	c.define()
	var r CanaryStartResult
	ce := wantCode(t, c.call(OpCanaryStart, c.role(), &r), CodeRefused)
	if !strings.HasPrefix(ce.Message, "holder-version-skew: the holder is another build") ||
		!strings.Contains(ce.Message, "holder build "+otherHolderBuild.Short()) ||
		!strings.Contains(ce.Message, "expected build "+testHolderBuild.Short()) {
		t.Fatalf("refusal %q", ce.Message)
	}
	if len(asked) != 1 || asked[0] != c.cfg.Holder.Executable {
		t.Fatalf("read %v, want the config's holder executable", asked)
	}
	if state, _, epoch, found := c.shown(); !found || state != "stopped" || epoch != 0 {
		t.Fatalf("shown %q epoch %d", state, epoch)
	}
}

// TestCanaryTicketChecksTheRunningHolder is the mixed-pair test of attach,
// through the real reader of the holder's holder.json in its state folder: an
// old holder that records no build, a holder of another build, and the same
// build. Only the same build is asked for a ticket.
func TestCanaryTicketChecksTheRunningHolder(t *testing.T) {
	c := newCanaryEnv(t)
	c.build = sameHolderBuild()
	c.build.Record = nil // buildid.ReadRecord
	c.up(submitted)
	layout, err := c.cfg.Layout()
	if err != nil {
		t.Fatal(err)
	}
	record := func(build string) {
		t.Helper()
		content := `{"version":1,"role":"throwaway-one/lead","epoch":1` + build + `}`
		if err := os.WriteFile(filepath.Join(layout.Holder(), buildid.RecordFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(id buildid.Identity) string {
		return `,"build":{"module":"` + id.Module + `","version":"` + id.Version + `","revision":"` + id.Revision + `"}`
	}
	for _, tc := range []struct {
		name, build, why string
	}{
		{"old holder, no build recorded", "", buildid.WhyNoRecord},
		{"holder of another build", build(otherHolderBuild), buildid.WhyDiffer},
	} {
		record(tc.build)
		var r CanaryTicketResult
		ce := wantCode(t, c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r), CodeRefused)
		if !strings.HasPrefix(ce.Message, "holder-version-skew: "+tc.why) || !strings.Contains(ce.Message, "expected build "+testHolderBuild.Short()) {
			t.Fatalf("%s: refusal %q", tc.name, ce.Message)
		}
		if n := len(c.holder.requests(wire.OpTicket)); n != 0 {
			t.Fatalf("%s: the holder was asked %d times", tc.name, n)
		}
	}
	record(build(testHolderBuild))
	var r CanaryTicketResult
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r)
	if n := len(c.holder.requests(wire.OpTicket)); n != 1 || r.Epoch != 1 {
		t.Fatalf("same build: %d requests, %+v", n, r)
	}
}

// TestLeadStartAndTicketRefuseASkewedHolder: in scratch mode the start of a
// lead whose holder executable is another build is refused with the code
// holder-version-skew and changes nothing; a running lead whose holder records
// no build gets no ticket, with the same code.
func TestLeadStartAndTicketRefuseASkewedHolder(t *testing.T) {
	l := newLeadEnv(t)
	other := sameHolderBuild()
	other.Executable = func(string) (buildid.Identity, error) { return otherHolderBuild, nil }
	l.scratch.HolderBuild = other
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	r := l.startLead("alpha")
	want(t, r, leadwire.StatusRefused, "holder-version-skew")
	if !strings.Contains(r.Message, "holder build "+otherHolderBuild.Short()) || !strings.Contains(r.Message, "expected build "+testHolderBuild.Short()) {
		t.Fatalf("message %q", r.Message)
	}
	if row := l.row("alpha"); row.Epoch != 0 || row.State != "stopped" {
		t.Fatalf("row %+v", row)
	}

	// The same build starts; then the running holder records no build.
	other.Executable = func(string) (buildid.Identity, error) { return testHolderBuild, nil }
	other.Record = func(string) (buildid.Record, error) { return buildid.Record{Epoch: 1}, nil }
	s := l.startLead("alpha")
	want(t, s, leadwire.StatusDone, "")
	tk := l.ticket("alpha", 1, "writer")
	want(t, tk, leadwire.StatusRefused, "holder-version-skew")
	if !strings.Contains(tk.Message, buildid.WhyNoRecord) {
		t.Fatalf("message %q", tk.Message)
	}
	if n := len(l.holders["alpha"].requests(wire.OpTicket)); n != 0 {
		t.Fatalf("the holder was asked %d times", n)
	}
}
