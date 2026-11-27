//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadwire"
)

// Put the project at another real test instance and leave the other projects
// of this root unenrolled.
func foreignProject(t *testing.T, state, project string) {
	t.Helper()
	peer, err := leadregistry.InitInstance(filepath.Join(t.TempDir(), "peer"), RegistryDirName, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leadregistry.InitInstance(state, RegistryDirName, ""); err != nil {
		t.Fatal(err)
	}
	r, err := leadregistry.Open(filepath.Join(state, RegistryDirName), leadregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRegistry(t, r)
	if err := r.SetProjectAssignment(project, peer.Authority, leadregistry.ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
}

func TestCanaryBusyAssignmentExit(t *testing.T) {
	var stderr bytes.Buffer
	err := &clientapi.Error{Code: clientapi.ErrorCode(leadregistry.ErrAssignmentBusy), Message: "project has a defined lead"}
	if code := canaryFailure("start", err, &stderr); code != ExitRefused || !strings.Contains(stderr.String(), err.Message) {
		t.Fatalf("busy assignment: exit %d: %s", code, stderr.String())
	}
}

func TestProjectHomeHandlersBeforeProvisioning(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	foreignProject(t, state, "remote")
	reg, err := leadregistry.Open(filepath.Join(state, RegistryDirName), leadregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRegistry(t, reg)
	m := &leadMode{reg: reg}
	for project, code := range map[string]leadregistry.HomeRefusal{"remote": leadregistry.ErrOtherHome, "missing": leadregistry.ErrHomeMissing} {
		role, err := leadrole.Lead(project)
		if err != nil {
			t.Fatal(err)
		}
		lr := &leadRole{role: role, op: make(chan struct{}, 1)}
		for _, out := range []outcome{m.define(t.Context(), lr), m.start(t.Context(), lr)} {
			if string(out.status) != string(leadwire.StatusRefused) || out.code != string(code) || out.msg != code.Error() {
				t.Fatalf("%s: %+v", role, out)
			}
		}
		x := &canaryExt{reg: reg, role: role, logf: func(string, ...any) {}}
		_, d := x.define()
		_, s := x.start(t.Context())
		for _, f := range []*clientapi.ErrorBody{d, s} {
			if f == nil || f.Code != clientapi.ErrorCode(code) || f.Message != code.Error() {
				t.Fatalf("%s: %+v", role, f)
			}
			if exit := canaryFailure("start", &clientapi.Error{Code: f.Code, Message: f.Message}, io.Discard); exit != ExitRefused {
				t.Fatalf("exit %d", exit)
			}
		}
	}
}

func TestProjectHomeScratchSocket(t *testing.T) {
	l := newLeadEnvWithoutHomes(t)
	foreignProject(t, l.state, l.cfgOf("alpha").Role.Project())
	l.start()
	if logs := l.log.String(); !strings.Contains(logs, "project scratch-beta has no home, blocking define and start for scratch-beta/lead") || strings.Contains(logs, "project scratch-alpha has no home") {
		t.Fatalf("startup enrollment warnings: %s", logs)
	}
	path := filepath.Join(l.state, RegistryDirName, leadregistry.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, code := range map[string]leadregistry.HomeRefusal{"alpha": leadregistry.ErrOtherHome, "beta": leadregistry.ErrHomeMissing} {
		for _, receipt := range []leadwire.Receipt{l.define(key), l.startLead(key)} {
			want(t, receipt, leadwire.StatusRefused, string(code))
			if code == leadregistry.ErrHomeMissing && !strings.Contains(receipt.Message, "project-home") {
				t.Fatalf("missing enrollment step: %q", receipt.Message)
			}
		}
		if row := l.row(key); row.Defined || row.Provision != leadwire.ProvisionNone {
			t.Fatalf("refused project: %+v", row)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || l.gate.starts.Load() != 0 {
		t.Fatal("refusal changed journal or started a unit")
	}
}

func TestCanaryMissingHomeStartupSocket(t *testing.T) {
	c := newCanaryEnvWithoutHome(t)
	c.start()
	if logs := c.log.String(); !strings.Contains(logs, "project throwaway-one has no home, blocking define and start for throwaway-one/lead") {
		t.Fatal(logs)
	}
}

func TestProjectHomeCanarySocket(t *testing.T) {
	c := newCanaryEnvWithoutHome(t)
	foreignProject(t, c.state, c.cfg.Role.Project())
	c.start()
	for _, op := range []string{OpCanaryDefine, OpCanaryStart} {
		ce := wantCode(t, c.call(op, c.role(), nil), clientapi.ErrorCode(leadregistry.ErrOtherHome))
		if ce.Message != leadregistry.ErrOtherHome.Error() {
			t.Fatalf("%s: %q", op, ce.Message)
		}
	}
}
