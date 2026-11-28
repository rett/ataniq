//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
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
// be the expected build, of whatever epoch is attached: the tests' holders are
// fakes, their executable does not exist and they write no state file.
func sameHolderBuild() *buildid.Checker {
	return &buildid.Checker{
		Expected:   testHolderBuild,
		Executable: func(string) (buildid.Identity, error) { return testHolderBuild, nil },
		Record: func(_ string, epoch uint64) (buildid.Record, error) {
			b := testHolderBuild
			return buildid.Record{Epoch: epoch, Build: &b}, nil
		},
	}
}

// TestHolderVersionSkewCodeIsBuildidReason: the receipt's code is the name
// the build check gives the refusal, and the code of a state file of another
// epoch is the name it gives that one.
func TestHolderVersionSkewCodeIsBuildidReason(t *testing.T) {
	if codeHolderVersionSkew != buildid.Reason {
		t.Fatalf("%q is not %q", codeHolderVersionSkew, buildid.Reason)
	}
	for _, name := range []string{buildid.EpochReason, leadwire.CodeStaleEpoch} {
		if codeStaleEpoch != name {
			t.Fatalf("%q is not %q", codeStaleEpoch, name)
		}
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
// old holder that records no build, a holder of another build, a state file
// of another epoch that records the same build, and the same build of the
// epoch. Only the same build of the epoch is asked for a ticket.
func TestCanaryTicketChecksTheRunningHolder(t *testing.T) {
	c := newCanaryEnv(t)
	c.build = sameHolderBuild()
	c.build.Record = nil // buildid.ReadRecord
	c.up(submitted)
	layout, err := c.cfg.Layout()
	if err != nil {
		t.Fatal(err)
	}
	record := func(epoch, build string) {
		t.Helper()
		content := `{"version":1,"role":"throwaway-one/lead","epoch":` + epoch + build + `}`
		if err := os.WriteFile(filepath.Join(layout.Holder(), buildid.RecordFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(id buildid.Identity) string {
		return `,"build":{"module":"` + id.Module + `","version":"` + id.Version + `","revision":"` + id.Revision + `"}`
	}
	for _, tc := range []struct {
		name, epoch, build, prefix string
	}{
		{"old holder, no build recorded", "1", "", "holder-version-skew: " + buildid.WhyNoRecord},
		{"holder of another build", "1", build(otherHolderBuild), "holder-version-skew: " + buildid.WhyDiffer},
		{"state file of the epoch before", "0", build(testHolderBuild), "stale-epoch: the holder's state file is of epoch 0, not of epoch 1"},
		{"state file of another epoch", "2", build(testHolderBuild), "stale-epoch: the holder's state file is of epoch 2, not of epoch 1"},
	} {
		record(tc.epoch, tc.build)
		var r CanaryTicketResult
		ce := wantCode(t, c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r), CodeRefused)
		if !strings.HasPrefix(ce.Message, tc.prefix) {
			t.Fatalf("%s: refusal %q", tc.name, ce.Message)
		}
		if strings.HasPrefix(tc.prefix, "holder-version-skew") && !strings.Contains(ce.Message, "expected build "+testHolderBuild.Short()) {
			t.Fatalf("%s: refusal %q", tc.name, ce.Message)
		}
		if n := len(c.holder.requests(wire.OpTicket)); n != 0 {
			t.Fatalf("%s: the holder was asked %d times", tc.name, n)
		}
	}
	record("1", build(testHolderBuild))
	var r CanaryTicketResult
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r)
	if n := len(c.holder.requests(wire.OpTicket)); n != 1 || r.Epoch != 1 {
		t.Fatalf("same build: %d requests, %+v", n, r)
	}
}

// TestLeadStartAndTicketRefuseASkewedHolder: in scratch mode the start of a
// lead whose holder executable is another build is refused with the code
// holder-version-skew and changes nothing; a running lead whose holder records
// no build gets no ticket, with the same code, and one whose state file is of
// another epoch gets none, with the code stale-epoch.
func TestLeadStartAndTicketRefuseASkewedHolder(t *testing.T) {
	l := newLeadEnv(t)
	other := sameHolderBuild()
	other.Executable = func(string) (buildid.Identity, error) { return otherHolderBuild, nil }
	l.scratch.HolderBuild = other
	// The ready observer of the start below gives up at once (the holder
	// gives no status), before the record is changed: one that still ran
	// would read the record of no build and show the lead degraded (see
	// lead_ready_linux.go), which this test is not about, while its reader
	// is being replaced.
	l.readyBounds(50 * time.Millisecond)
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
	s := l.startLead("alpha")
	want(t, s, leadwire.StatusDone, "")
	l.waitLog("the lead stays starting")
	other.Record = func(string, uint64) (buildid.Record, error) { return buildid.Record{Epoch: 1}, nil }
	tk := l.ticket("alpha", 1, "writer")
	want(t, tk, leadwire.StatusRefused, "holder-version-skew")
	if !strings.Contains(tk.Message, buildid.WhyNoRecord) {
		t.Fatalf("message %q", tk.Message)
	}
	if n := len(l.holders["alpha"].requests(wire.OpTicket)); n != 0 {
		t.Fatalf("the holder was asked %d times", n)
	}
	// A message is checked as a ticket is, and the refusal spends nothing.
	in := l.inject("alpha", 1, "msg-one", "body one")
	want(t, in, leadwire.StatusRefused, "holder-version-skew")
	if !strings.Contains(in.Message, buildid.WhyNoRecord) {
		t.Fatalf("message %q", in.Message)
	}

	// A state file of the epoch before, of the same build: the holder of the
	// epoch has not written its own, and the ticket is refused stale-epoch.
	other.Record = func(string, uint64) (buildid.Record, error) {
		b := testHolderBuild
		return buildid.Record{Epoch: 0, Build: &b}, nil
	}
	tk = l.ticket("alpha", 1, "writer")
	want(t, tk, leadwire.StatusRefused, "stale-epoch")
	if !strings.Contains(tk.Message, "of epoch 0, not of epoch 1") {
		t.Fatalf("message %q", tk.Message)
	}
	in = l.inject("alpha", 1, "msg-one", "body one")
	want(t, in, leadwire.StatusRefused, "stale-epoch")
	if !strings.Contains(in.Message, "of epoch 0, not of epoch 1") {
		t.Fatalf("message %q", in.Message)
	}
	h := l.holders["alpha"]
	if n := len(h.requests(wire.OpTicket)) + len(h.requests(wire.OpInject)); n != 0 {
		t.Fatalf("the holder was asked %d times", n)
	}
	if row := l.row("alpha"); row.MessageSpent {
		t.Fatal("a refused message spent the epoch")
	}

	// The holder of the epoch records the same build: the message goes.
	other.Record = sameHolderBuild().Record
	want(t, l.inject("alpha", 1, "msg-one", "body one"), leadwire.StatusDone, "")
	if n := len(h.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject frames", n)
	}
}

// TestCanaryInjectChecksTheRunningHolder: the canary's message is checked
// as its ticket is, through the real reader of holder.json: a holder of
// another build is refused by name before any frame is written, with an
// inject-refused line of the code, and the epoch's message is not spent; the
// same build of the epoch then takes it.
func TestCanaryInjectChecksTheRunningHolder(t *testing.T) {
	c := newCanaryEnv(t)
	c.build = sameHolderBuild()
	c.build.Record = nil // buildid.ReadRecord
	started := c.up(submitted)
	layout, err := c.cfg.Layout()
	if err != nil {
		t.Fatal(err)
	}
	record := func(id buildid.Identity) {
		t.Helper()
		content := `{"version":1,"role":"throwaway-one/lead","epoch":` + strconv.FormatUint(started.Epoch, 10) +
			`,"build":{"module":"` + id.Module + `","version":"` + id.Version + `","revision":"` + id.Revision + `"}}`
		if err := os.WriteFile(filepath.Join(layout.Holder(), buildid.RecordFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record(otherHolderBuild)
	_, err = c.inject("msg-one", "body text\n")
	ce := wantCode(t, err, CodeRefused)
	if !strings.HasPrefix(ce.Message, "holder-version-skew: "+buildid.WhyDiffer) {
		t.Fatalf("refusal %q", ce.Message)
	}
	if l := c.lastEvidence(); l["event"] != "inject-refused" || l["code"] != "holder-version-skew" || l["message_id"] != "msg-one" {
		t.Fatalf("line %v", l)
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("%d inject frames reached the holder", n)
	}
	record(testHolderBuild)
	got, err := c.inject("msg-one", "body text\n")
	if err != nil || got.Epoch != started.Epoch || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}

// holderRecord writes the holder's holder.json in the state folder of the lead
// whose config is cfg: the epoch and, unless build is nil, the build.
func holderRecord(t *testing.T, cfg leadcanary.Config, epoch uint64, build *buildid.Identity) {
	t.Helper()
	layout, err := cfg.Layout()
	if err != nil {
		t.Fatal(err)
	}
	content := `{"version":1,"role":"` + cfg.Role.String() + `","epoch":` + strconv.FormatUint(epoch, 10)
	if build != nil {
		content += `,"build":{"module":"` + build.Module + `","version":"` + build.Version + `","revision":"` + build.Revision + `"}`
	}
	if err := os.WriteFile(filepath.Join(layout.Holder(), buildid.RecordFileName), []byte(content+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLeadStartingWithoutStateFileIsNotReady: a lead shown starting whose
// holder has not written holder.json yet (a first start) is not a holder of
// another build: a ticket and a message asked before the file is there are
// refused stale-epoch with the text that the role is still starting, nothing
// reaches the holder and the message is not spent; once the holder has
// written its file, the same ticket and message go through.
func TestLeadStartingWithoutStateFileIsNotReady(t *testing.T) {
	l := newLeadEnv(t)
	l.scratch.HolderBuild.Record = nil // buildid.ReadRecord, over the real state folder
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	h := l.holder("alpha", submitted) // up, but never says ready: the lead stays starting
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.State != "starting" || row.Epoch != 1 {
		t.Fatalf("row %+v", row)
	}
	// Before the holder writes its file.
	tk := l.ticket("alpha", 1, "writer")
	want(t, tk, leadwire.StatusRefused, "stale-epoch")
	in := l.inject("alpha", 1, "msg-one", "body one")
	want(t, in, leadwire.StatusRefused, "stale-epoch")
	for _, r := range []leadwire.Receipt{tk, in} {
		if r.Message != msgNotReady {
			t.Fatalf("message %q, want %q", r.Message, msgNotReady)
		}
	}
	if n := len(h.requests(wire.OpTicket)) + len(h.requests(wire.OpInject)); n != 0 {
		t.Fatalf("the holder was asked %d times", n)
	}
	if row := l.row("alpha"); row.MessageSpent {
		t.Fatal("a refused message spent the epoch")
	}
	// After it.
	b := testHolderBuild
	holderRecord(t, l.cfgOf("alpha"), 1, &b)
	want(t, l.ticket("alpha", 1, "writer"), leadwire.StatusDone, "")
	want(t, l.inject("alpha", 1, "msg-one", "body one"), leadwire.StatusDone, "")
	if n, m := len(h.requests(wire.OpTicket)), len(h.requests(wire.OpInject)); n != 1 || m != 1 {
		t.Fatalf("%d tickets, %d messages reached the holder", n, m)
	}
}

// TestCanaryStartingWithoutStateFileIsNotReady: the canary's lead stays
// starting, and a ticket asked before its holder has written holder.json is
// refused as not ready, not as another build; after the file is written it
// is served.
func TestCanaryStartingWithoutStateFileIsNotReady(t *testing.T) {
	c := newCanaryEnv(t)
	c.build = sameHolderBuild()
	c.build.Record = nil // buildid.ReadRecord
	started := c.up(submitted)
	var r CanaryTicketResult
	ce := wantCode(t, c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r), CodeRefused)
	if ce.Message != msgNotReady {
		t.Fatalf("refusal %q", ce.Message)
	}
	_, err := c.inject("msg-one", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgNotReady {
		t.Fatalf("refusal %q", ce.Message)
	}
	if l := c.lastEvidence(); l["event"] != "inject-refused" || l["code"] != "stale-epoch" {
		t.Fatalf("line %v", l)
	}
	if n := len(c.holder.requests(wire.OpTicket)) + len(c.holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("the holder was asked %d times", n)
	}
	b := testHolderBuild
	holderRecord(t, c.cfg, started.Epoch, &b)
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &r)
	if got, err := c.inject("msg-one", "body text\n"); err != nil || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}
