//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"path"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
	sbprofile "ataniq/internal/leadsandbox/profile"
)

// reachCovers reports whether p is a path of the reach list or lies below one.
func reachCovers(reach []LeadReach, p string) bool {
	for _, r := range reach {
		if p == r.Path || strings.HasPrefix(p, r.Path+"/") {
			return true
		}
	}
	return false
}

// Every source of every bind of a scratch lead's bind table, built as the
// holder builds it from the inputs provisioning writes (provision.BuildInputs
// and the bound folder of the holder's runtime folder), lies in or is a path of
// ScratchLeadReach: a bind added to the table without a place in the list
// fails here. The list names every lead of the config, defined or not, and
// reads no file.
func TestScratchLeadReachCoversEveryBind(t *testing.T) {
	dir, err := os.MkdirTemp("", "lr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	w := provisiontest.NewScratch(shortTB{TB: t, dir: dir})
	reach, err := ScratchLeadReach(w.Config)
	if err != nil {
		t.Fatalf("ScratchLeadReach: %v", err)
	}
	for _, r := range reach {
		if r.Path == "" || path.Clean(r.Path) != r.Path || !path.IsAbs(r.Path) || r.What == "" {
			t.Errorf("a reach path is not a clean absolute path with a reason: %+v", r)
		}
	}
	leads, err := w.Config.Leads()
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != len(provisiontest.ScratchKeys) {
		t.Fatalf("the world has %d leads", len(leads))
	}
	for i, cfg := range leads {
		key := w.Config.Entries[i].Key
		in, err := provision.BuildInputs(cfg, w.Config.Sandbox, strings.Repeat("c1", 32))
		if err != nil {
			t.Fatalf("BuildInputs %s: %v", key, err)
		}
		rt, err := sbprofile.ParseRuntime(in.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		// The holder's profile inputs (sandboxstart's profileInputs): the
		// spec folder and the resolv.conf lie in the bound folder of the
		// holder's runtime folder.
		bound := path.Join(sandboxstart.RuntimeFolder(in.RuntimeRoot, in.LayoutRole), "bound")
		pin := sbprofile.Inputs{
			StateRoot: in.StateRoot, Role: in.LayoutRole, Project: in.Project,
			Runtime: rt, RuntimeVersion: in.RuntimeVersion, Identity: sbprofile.IdentityRoot,
			GitMeta:      sbprofile.Layout{StateRoot: in.StateRoot, Role: in.LayoutRole, Project: in.Project}.GitMeta(),
			AgentInstall: in.Sources.AgentInstall, ProbeBinary: in.Executables.Probe,
			SpecDir:         path.Join(bound, "spec"),
			ManagedSettings: in.Sources.ManagedSettings, ManagedSettingsInside: in.Sources.ManagedSettingsInside,
			ManagedSettingsSHA256: in.Sources.ManagedSettingsSHA256,
			LeadSkills:            in.Sources.LeadSkills, InstructionFile: in.Sources.InstructionFile,
			GitIdentityDir: in.Sources.GitIdentityDir, QargiInstall: in.Sources.QargiInstall,
			QargiRecordsInside: in.Sources.QargiRecordsInside,
			AssignmentDir:      in.Sources.AssignmentDir, ProjectionDir: in.Sources.ProjectionDir,
			ResolvConf:   path.Join(bound, "resolv.conf"),
			NeverMounted: in.NeverMounted,
			Term:         in.Terminal.Term, ColorTerm: in.Terminal.ColorTerm, Lang: in.Terminal.Lang, TZ: in.Terminal.TZ,
		}
		binds, err := pin.Binds()
		if err != nil {
			t.Fatalf("the bind table of %s: %v", key, err)
		}
		n := 0
		for _, b := range binds {
			if b.Kind != sbprofile.KindRoBind && b.Kind != sbprofile.KindRwBind {
				continue
			}
			n++
			if !reachCovers(reach, b.Source) {
				t.Errorf("the lead %s binds %s (%s at %s), which ScratchLeadReach does not name", key, b.Source, b.SourceRole, b.Inside)
			}
		}
		if n < 10 {
			t.Fatalf("the bind table of %s has %d binds", key, n)
		}
		// The lead's own folders are named by its key.
		p, err := provision.PathsOf(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{p.LeadDir, p.Mirror, p.Mailbox, p.Published, p.RuntimeFolder} {
			found := false
			for _, r := range reach {
				if r.Path == want && strings.Contains(r.What, "the lead "+key+":") {
					found = true
				}
			}
			if !found {
				t.Errorf("ScratchLeadReach does not name %s for the lead %s", want, key)
			}
		}
	}
	// A config the daemon refuses gives no list.
	bad := w.Config
	bad.Entries = append(bad.Entries[:0:0], bad.Entries...)
	bad.Entries[0].Key = "Not a key"
	if _, err := ScratchLeadReach(bad); err == nil {
		t.Error("ScratchLeadReach took a config whose lead key is not valid")
	}
}
