//go:build linux && ataniq_canary

package leadcanary_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// scratchConfig returns a valid config of a scratch lead whose paths lie in the
// fixture.
func (f fixture) scratchConfig(t *testing.T, project string) leadcanary.Config {
	t.Helper()
	cfg := f.config(t)
	cfg.Role = mustRole(t, project+"/lead")
	cfg.Reserved = leadrole.ReservedScratch
	cfg.Provision.Source = filepath.Join(f.dir, "upstream", "sample.git")
	return cfg
}

func TestNamesForScratch(t *testing.T) {
	role := mustRole(t, "scratch-alpha/lead")
	layoutRole, unit, err := leadcanary.NamesFor(leadrole.ReservedScratch, role)
	if err != nil {
		t.Fatal(err)
	}
	if layoutRole != "scratch-alpha-lead" || unit != "ataniq-lead-scratch-alpha.service" {
		t.Fatalf("got %q and %q", layoutRole, unit)
	}
	back, err := leadcanary.RoleOfFor(leadrole.ReservedScratch, layoutRole)
	if err != nil || back != role {
		t.Fatalf("RoleOfFor gave %v, %v", back, err)
	}
	// 50 bytes of key is the longest that fits: scratch- (8) + 50 + -lead (5) = 63.
	long := mustRole(t, "scratch-"+strings.Repeat("a", 50)+"/lead")
	if _, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, long); err != nil {
		t.Fatalf("the longest key: %v", err)
	}
	tooLong := mustRole(t, "scratch-"+strings.Repeat("a", 51)+"/lead")
	if _, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, tooLong); err == nil {
		t.Fatal("a layout name of 64 bytes was accepted")
	}
}

// TestTheTwoKindsRefuseEachOther holds that a role of one reserved kind is not
// a role of the other, in both directions and in both mappings, and that the
// plain functions are the throwaway kind.
func TestTheTwoKindsRefuseEachOther(t *testing.T) {
	throwaway, scratch := mustRole(t, "throwaway-one/lead"), mustRole(t, "scratch-one/lead")
	if _, _, err := leadcanary.Names(scratch); !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Errorf("Names(scratch role): %v", err)
	}
	if _, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, throwaway); !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Errorf("NamesFor(scratch, throwaway role): %v", err)
	}
	if _, err := leadcanary.RoleOf("scratch-one-lead"); !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Errorf("RoleOf(scratch layout role): %v", err)
	}
	if _, err := leadcanary.RoleOfFor(leadrole.ReservedScratch, "throwaway-one-lead"); !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Errorf("RoleOfFor(scratch, throwaway layout role): %v", err)
	}
	if _, _, err := leadcanary.NamesFor(leadrole.Reserved(7), scratch); err == nil {
		t.Error("a value that is not a kind accepted a role")
	}
	// The throwaway refusal keeps its text.
	_, _, err := leadcanary.Names(mustRole(t, "demo/lead"))
	if err == nil || err.Error() != leadcanary.ErrNotCanaryRole.Error() {
		t.Errorf("the throwaway refusal text changed: %v", err)
	}
}

func TestValidateOfAScratchConfig(t *testing.T) {
	f := newFixture(t)
	cfg := f.scratchConfig(t, "scratch-alpha")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	l, err := cfg.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if l.Role != "scratch-alpha-lead" || l.Project != "scratch-alpha" || l.StateRoot != f.state {
		t.Fatalf("layout %+v", l)
	}
	rt, err := cfg.HolderRuntimeDir()
	if err != nil || rt != filepath.Join(f.runtime, "leads", "scratch-alpha-lead") {
		t.Fatalf("runtime dir %q, %v", rt, err)
	}

	// A scratch config needs the scratch prefix, and a source.
	wrongKind := cfg
	wrongKind.Role = sampleRole(t)
	wantConfigError(t, wrongKind.Validate(), "role")
	noSource := cfg
	noSource.Provision.Source = ""
	wantConfigError(t, noSource.Validate(), "provision.source")
	relSource := cfg
	relSource.Provision.Source = "upstream/sample.git"
	wantConfigError(t, relSource.Validate(), "provision.source")

	// A throwaway config has no source and the scratch prefix is refused.
	throwaway := f.config(t)
	throwaway.Provision.Source = "/srv/upstream/sample.git"
	wantConfigError(t, throwaway.Validate(), "provision.source")
	throwaway = f.config(t)
	throwaway.Role = mustRole(t, "scratch-alpha/lead")
	wantConfigError(t, throwaway.Validate(), "role")
}

// TestTheCanaryConfigFileIsUnchanged holds that the new fields are not keys of
// the canary's file: a file that names either is refused, and a config does not
// write them.
func TestTheCanaryConfigFileIsUnchanged(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	m := tree(t, cfg)
	if _, ok := m["Reserved"]; ok {
		t.Error("the config writes a Reserved key")
	}
	prov := m["provision"].(map[string]any)
	if _, ok := prov["source"]; ok {
		t.Error("the config writes provision.source")
	}
	prov["source"] = "/srv/upstream/sample.git"
	_, err := leadcanary.ReadConfig(f.writeTree(t, m))
	wantConfigError(t, err, "file")
	if _, err := leadcanary.ReadConfig(f.writeConfig(t, cfg)); err != nil {
		t.Fatal(err)
	}
}

func TestEvidencePathOfAScratchLead(t *testing.T) {
	f := newFixture(t)
	if got, want := f.config(t).EvidencePath(), filepath.Join(f.state, "canary-evidence.jsonl"); got != want {
		t.Fatalf("the canary's path is %q, want %q", got, want)
	}
	a, b := f.scratchConfig(t, "scratch-alpha"), f.scratchConfig(t, "scratch-beta")
	if got, want := a.EvidencePath(), filepath.Join(f.state, "evidence", "scratch-alpha-lead.jsonl"); got != want {
		t.Fatalf("alpha: %q, want %q", got, want)
	}
	if a.EvidencePath() == b.EvidencePath() {
		t.Fatal("two scratch leads share an evidence file")
	}
}

func TestScratchLeadsKeepSeparateEvidence(t *testing.T) {
	f := newFixture(t)
	a, b := f.scratchConfig(t, "scratch-alpha"), f.scratchConfig(t, "scratch-beta")
	evA, err := leadcanary.OpenEvidence(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer evA.Close()
	evB, err := leadcanary.OpenEvidence(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer evB.Close()
	if err := evA.Append(leadcanary.Entry{Event: leadcanary.EventDefine}); err != nil {
		t.Fatal(err)
	}
	if err := evB.Append(leadcanary.Entry{Event: leadcanary.EventStop}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cfg  leadcanary.Config
		want string
	}{{a, `"role":"scratch-alpha/lead"`}, {b, `"role":"scratch-beta/lead"`}} {
		data, err := os.ReadFile(c.cfg.EvidencePath())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), c.want) || strings.Count(string(data), "\n") != 1 {
			t.Errorf("%s holds %q", c.cfg.EvidencePath(), data)
		}
	}
	info, err := os.Stat(filepath.Join(f.state, "evidence"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the evidence folder: %v, %v", info, err)
	}
	// The canary's file is not made.
	if _, err := os.Lstat(filepath.Join(f.state, leadcanary.EvidenceName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the canary's evidence file exists: %v", err)
	}
}

func TestOpenEvidenceRefusesAnUnsafeScratchFolder(t *testing.T) {
	cases := map[string]func(t *testing.T, f fixture, dir string){
		"a symbolic link": func(t *testing.T, f fixture, dir string) {
			if err := os.Symlink(f.conf, dir); err != nil {
				t.Fatal(err)
			}
		},
		"a file": func(t *testing.T, f fixture, dir string) {
			writeFile(t, dir, nil, 0o600)
		},
		"open to others": func(t *testing.T, f fixture, dir string) {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			cfg := f.scratchConfig(t, "scratch-alpha")
			setup(t, f, filepath.Join(f.state, "evidence"))
			if ev, err := leadcanary.OpenEvidence(cfg, nil); err == nil {
				ev.Close()
				t.Fatal("opened the evidence of a lead in an unsafe folder")
			}
		})
	}
}

func TestPreflightOfAScratchLead(t *testing.T) {
	scratch, throwaway := mustRole(t, "scratch-alpha/lead"), sampleRole(t)
	p, err := leadcanary.NewPreflightFor(leadrole.ReservedScratch, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Check(context.Background(), scratch, leadregistry.Entry{Role: scratch}); err != nil {
		t.Fatalf("the scratch role: %v", err)
	}
	if err := p.Check(context.Background(), throwaway, leadregistry.Entry{Role: throwaway}); !errors.Is(err, leadcanary.ErrPreflightRefused) {
		t.Fatalf("a throwaway role passed a scratch preflight: %v", err)
	}
	other := mustRole(t, "scratch-beta/lead")
	if err := p.Check(context.Background(), other, leadregistry.Entry{Role: other}); !errors.Is(err, leadcanary.ErrPreflightRefused) {
		t.Fatalf("another scratch role passed: %v", err)
	}
	canary, err := leadcanary.NewPreflight(throwaway)
	if err != nil {
		t.Fatal(err)
	}
	if err := canary.Check(context.Background(), scratch, leadregistry.Entry{Role: scratch}); !errors.Is(err, leadcanary.ErrPreflightRefused) {
		t.Fatalf("a scratch role passed the canary's preflight: %v", err)
	}
	if _, err := leadcanary.NewPreflightFor(leadrole.ReservedScratch, throwaway); err == nil {
		t.Error("NewPreflightFor(scratch) accepted a throwaway role")
	}
	if _, err := leadcanary.NewPreflight(scratch); err == nil {
		t.Error("NewPreflight accepted a scratch role")
	}
}

func TestAdmissionOfAScratchLead(t *testing.T) {
	scratch := mustRole(t, "scratch-alpha/lead")
	if _, err := leadcanary.NewAdmission(scratch); err == nil {
		t.Fatal("the canary's admission accepted a scratch role")
	}
	a, err := leadcanary.NewAdmissionFor(leadrole.ReservedScratch, scratch)
	if err != nil {
		t.Fatal(err)
	}
	text, err := a.Admit("msg-1", "hello")
	if err != nil || !strings.HasPrefix(text, "[ataniq-msg id=msg-1 from=ataniq kind=canary]\n") {
		t.Fatalf("Admit gave %q, %v", text, err)
	}
	if _, err := leadcanary.NewAdmissionFor(leadrole.ReservedScratch, sampleRole(t)); err == nil {
		t.Fatal("a scratch admission accepted a throwaway role")
	}
}

func TestHolderCommandOfAScratchLead(t *testing.T) {
	f := newFixture(t)
	cfg := f.scratchConfig(t, "scratch-alpha")
	h, err := leadcanary.NewHolderCommand(cfg, filepath.Join(f.state, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	role := cfg.Role
	argv, workdir, _, err := h.Build(role, leadregistry.Entry{Role: role, Epoch: 1}, leadregistry.Intent{
		ID: "intent-1", Epoch: 1, UnitName: role.UnitName(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if workdir != filepath.Join(f.state, "leads", "scratch-alpha-lead", "holder") {
		t.Errorf("workdir %q", workdir)
	}
	line := strings.Join(argv, " ")
	for _, want := range []string{
		"--role scratch-alpha/lead", "--canary", "--runtime-dir " + filepath.Join(f.runtime, "leads", "scratch-alpha-lead"),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the holder line lacks %q: %s", want, line)
		}
	}
}
