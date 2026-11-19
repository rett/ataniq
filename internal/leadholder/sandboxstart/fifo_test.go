//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"path"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/plan"
)

// TestReportFIFOPlace checks where the report FIFO is: in the runtime root,
// in a folder of its own, and never under the state root's leads.
func TestReportFIFOPlace(t *testing.T) {
	w := newWorld(t)
	got := ReportFIFO(w.run, sampleRole)
	if want := w.run + "/leads/" + sampleRole + "/report/probe.fifo"; got != want {
		t.Fatalf("ReportFIFO = %s, want %s", got, want)
	}
	if ReportFolder(w.run, sampleRole) != path.Dir(got) {
		t.Errorf("the report folder is %s, not the folder of the FIFO", ReportFolder(w.run, sampleRole))
	}
	if strings.HasPrefix(got, w.state+"/leads") || strings.HasPrefix(got, w.state+"/") {
		t.Errorf("the FIFO %s is under the state root", got)
	}
	if RuntimeFolder(w.run, sampleRole) != w.run+"/leads/"+sampleRole {
		t.Errorf("RuntimeFolder = %s", RuntimeFolder(w.run, sampleRole))
	}
}

// TestPlanAcceptsTheFIFOAndRefusesOneUnderTheStateLeads builds the plan of a
// start with the path Run passes, which is accepted, and with the same name
// under <state root>/leads, which the plan refuses.
func TestPlanAcceptsTheFIFOAndRefusesOneUnderTheStateLeads(t *testing.T) {
	w := newWorld(t)
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	var report *plan.File
	for i := range st.Plan.Files {
		if st.Plan.Files[i].Purpose == plan.PurposeReport {
			report = &st.Plan.Files[i]
		}
	}
	if report == nil || report.Path != ReportFIFO(w.run, sampleRole) || report.FD != ReportFD || report.Mode != plan.FileWrite {
		t.Fatalf("the plan's report file is %+v, want the FIFO at fd %d, write-only", report, ReportFD)
	}
	// The plan's own check accepts it (Run built and verified it); build it
	// again from the same options to be sure that the path alone decides.
	a := &attempt{ctx: context.Background(), in: w.in, sys: w.sys, launch: w.lch}
	steps := []func() error{a.validate, a.resolv, a.resolveSources, a.buildPlan}
	for i, s := range steps {
		if err := s(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	defer a.closeAll()
	if _, err := plan.Build(a.pin, a.planOpt); err != nil {
		t.Fatalf("the runtime root's FIFO is refused: %v", err)
	}
	bad := a.planOpt
	bad.ReportDir = path.Join(w.state, "leads", sampleRole, "report")
	bad.ReportPath = path.Join(bad.ReportDir, reportFIFOName)
	_, err = plan.Build(a.pin, bad)
	if err == nil {
		t.Fatal("a report path under <state root>/leads is accepted")
	}
	if !strings.Contains(err.Error(), "report path") {
		t.Errorf("the refusal does not name the report path: %v", err)
	}
}

// TestRunRefusesRootsThatNestWithoutCanary shows the report FIFO cannot come to
// lie under the state root's leads even with canary false, where the root check
// is not made: the inputs refuse nested roots.
func TestRunRefusesRootsThatNestWithoutCanary(t *testing.T) {
	w := newWorld(t)
	w.in.Canary = false
	w.in.RuntimeRoot = w.state
	_, err := w.run_()
	if err == nil || !strings.Contains(err.Error(), "runtime_root") {
		t.Fatalf("error %v, want a refusal of the runtime root", err)
	}
	if w.lch.launches() != 0 {
		t.Error("launched")
	}
}
