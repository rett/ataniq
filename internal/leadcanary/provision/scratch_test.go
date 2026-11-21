//go:build linux && ataniq_canary

package provision

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/sandboxstart"
)

// defineLead runs ProvisionScratch for lead i of the world.
func defineLead(t *testing.T, w *provisiontest.ScratchWorld, cfg leadcanary.Config) (ScratchResult, []string, error) {
	t.Helper()
	var lines []string
	res, err := ProvisionScratch(context.Background(), cfg, w.Config.Sandbox,
		Options{Getenv: w.Getenv(), Log: func(s string) { lines = append(lines, s) }})
	return res, lines, err
}

func mustDefine(t *testing.T, w *provisiontest.ScratchWorld, cfg leadcanary.Config) ScratchResult {
	t.Helper()
	res, lines, err := defineLead(t, w, cfg)
	if err != nil {
		t.Fatalf("ProvisionScratch: %v\nlog: %q", err, lines)
	}
	return res
}

func TestProvisionScratchMakesTheLeadAndItsRecord(t *testing.T) {
	w := provisiontest.NewScratch(t)
	leads := w.Leads(t)
	cfg := leads[0]
	res := mustDefine(t, w, cfg)
	p, err := PathsOf(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The folders of a canary lead are there, and so is the lead's own folder
	// of the scratch folder.
	for _, d := range []string{p.LeadDir, p.Tree, p.Home, p.Mailbox, p.Published, p.RoleDir, p.RuntimeFolder, p.ScratchRoot, p.ScratchDir, p.MirrorsDir} {
		if fi := lstat(t, d); !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", d, fi.Mode())
		}
	}
	// The mirror was made from the source and holds the branch.
	if got := provisiontest.Git(t, p.Mirror, "--git-dir=.", "rev-parse", "refs/heads/"+provisiontest.Branch); got != w.Head {
		t.Errorf("the mirror's branch is %s, want %s", got, w.Head)
	}
	if got := provisiontest.Git(t, p.Tree, "rev-parse", "HEAD"); got != w.Head {
		t.Errorf("the tree is at %s, want %s", got, w.Head)
	}
	// No canary marker in the root, and the root marker is the owner's.
	if exists(filepath.Join(w.State, canaryroot.MarkerName)) {
		t.Error("provisioning wrote the canary's marker in a scratch place")
	}
	if err := canaryroot.CheckMarker(w.State, cfg.Role.Project()); err != nil {
		t.Errorf("the scratch root marker: %v", err)
	}

	// The inputs file is the lead's, and passes the check against the config.
	fi := lstat(t, p.SandboxInputs)
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("the inputs file is %v", fi.Mode())
	}
	if err := CheckInputs(cfg, p.SandboxInputs); err != nil {
		t.Fatalf("CheckInputs: %v", err)
	}
	// The digest is the holder's own computation over that file.
	in, err := sandboxstart.ReadInputs(p.SandboxInputs)
	if err != nil {
		t.Fatal(err)
	}
	in.AgentEnv = sandboxstart.AgentEnvOf(cfg.Agent.Env)
	want, err := sandboxstart.ProfileDigest(in, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	if res.ProfileDigest != want || len(want) != leadcanary.DigestLen {
		t.Errorf("the result's digest is %q, the holder's is %q", res.ProfileDigest, want)
	}

	// The record is last, holds the digest and reads back.
	rec, err := ReadLeadRecord(cfg)
	if err != nil || rec.ProfileDigest != want {
		t.Fatalf("ReadLeadRecord: %+v, %v", rec, err)
	}
	if fi := lstat(t, p.LeadRecord); fi.Mode().Perm() != 0o600 {
		t.Errorf("the record is %v", fi.Mode())
	}
	if st, err := ScratchStateOf(cfg); err != nil || st != StateDone {
		t.Fatalf("state %v, %v", st, err)
	}
	// The second lead has not been touched.
	if st, err := ScratchStateOf(leads[1]); err != nil || st != StateNone {
		t.Fatalf("the other lead: state %v, %v", st, err)
	}
}

// TestTwoLeadsShareOneRoot defines two leads in one state root and holds that
// each has its own folders, inputs, record, digest and mirror, and that a lead
// of one does not disturb the other.
func TestTwoLeadsShareOneRoot(t *testing.T) {
	w := provisiontest.NewScratch(t)
	leads := w.Leads(t)
	a, b := mustDefine(t, w, leads[0]), mustDefine(t, w, leads[1])
	pa, _ := PathsOf(leads[0])
	pb, _ := PathsOf(leads[1])
	for _, pair := range [][2]string{
		{pa.LeadDir, pb.LeadDir}, {pa.SandboxInputs, pb.SandboxInputs}, {pa.LeadRecord, pb.LeadRecord},
		{pa.Mirror, pb.Mirror}, {pa.RoleDir, pb.RoleDir}, {pa.RuntimeFolder, pb.RuntimeFolder},
	} {
		if pair[0] == pair[1] {
			t.Errorf("two leads share %s", pair[0])
		}
		if !exists(pair[0]) || !exists(pair[1]) {
			t.Errorf("%s or %s was not made", pair[0], pair[1])
		}
	}
	if len(a.ProfileDigest) != leadcanary.DigestLen || len(b.ProfileDigest) != leadcanary.DigestLen {
		t.Errorf("digests %q and %q", a.ProfileDigest, b.ProfileDigest)
	}
	for _, c := range leads {
		if err := CheckInputs(c, c.Holder.SandboxInputs); err != nil {
			t.Errorf("%v: %v", c.Role, err)
		}
		if st, _ := ScratchStateOf(c); st != StateDone {
			t.Errorf("%v: state %v", c.Role, st)
		}
	}
}

func TestProvisionScratchRefusesWhatIsNotScratch(t *testing.T) {
	w := provisiontest.NewScratch(t)
	canary := provisiontest.New(t)
	if _, err := ProvisionScratch(context.Background(), canary.Cfg, w.Config.Sandbox, Options{Getenv: canary.Getenv()}); !errors.Is(err, ErrScratchKind) {
		t.Errorf("ProvisionScratch of a canary config: %v", err)
	}
	if err := Provision(context.Background(), w.Leads(t)[0], Options{Getenv: w.Getenv()}); !errors.Is(err, ErrScratchKind) {
		t.Errorf("Provision of a scratch config: %v", err)
	}
	for name, f := range map[string]func() error{
		"BuildInputs":  func() error { _, err := BuildInputs(canary.Cfg, w.Config.Sandbox, hashHex(nil)); return err },
		"WriteInputs":  func() error { return WriteInputs(canary.Cfg, w.Config.Sandbox, hashHex(nil)) },
		"ReadRecord":   func() error { _, err := ReadLeadRecord(canary.Cfg); return err },
		"ScratchState": func() error { _, err := ScratchStateOf(canary.Cfg); return err },
		"CleanScratch": func() error { return CleanScratch(canary.Cfg, Options{}) },
	} {
		if err := f(); !errors.Is(err, ErrScratchKind) {
			t.Errorf("%s of a canary config: %v", name, err)
		}
	}
	// Nothing was made by the refusals.
	if exists(filepath.Join(w.State, "mirrors")) || exists(filepath.Join(w.State, "scratch")) {
		t.Error("a refused call made folders")
	}
}

func TestProvisionScratchNeedsTheOwnersRootMarker(t *testing.T) {
	cases := map[string]func(t *testing.T, w *provisiontest.ScratchWorld){
		"no marker": func(t *testing.T, w *provisiontest.ScratchWorld) {
			if err := os.Remove(filepath.Join(w.State, canaryroot.ScratchMarkerName)); err != nil {
				t.Fatal(err)
			}
		},
		"the canary's marker": func(t *testing.T, w *provisiontest.ScratchWorld) {
			provisiontest.WriteFile(t, filepath.Join(w.State, canaryroot.MarkerName), []byte("x"), 0o600)
		},
		"a marker with other content": func(t *testing.T, w *provisiontest.ScratchWorld) {
			provisiontest.WriteFile(t, filepath.Join(w.State, canaryroot.ScratchMarkerName), []byte("{}\n"), 0o600)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.NewScratch(t)
			setup(t, w)
			cfg := w.Leads(t)[0]
			_, _, err := defineLead(t, w, cfg)
			var re *canaryroot.RefusedError
			if !errors.As(err, &re) {
				t.Fatalf("got %v, want a root refusal", err)
			}
			if exists(filepath.Join(w.State, "mirrors")) || exists(filepath.Join(w.State, "leads")) {
				t.Error("a lead was provisioned in a place without the owner's marker")
			}
		})
	}
}

func TestProvisionScratchRefusesADefaultRoot(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	home := t.TempDir()
	inside := filepath.Join(home, ".local", "state", "ataniq", "scratch")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := canaryroot.WriteScratchMarker(inside); err != nil {
		t.Fatal(err)
	}
	cfg.StateRoot = inside
	_, err := ProvisionScratch(context.Background(), cfg, w.Config.Sandbox, Options{Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}})
	var re *canaryroot.RefusedError
	if !errors.As(err, &re) || re.Code != canaryroot.CodeDefaultRoot {
		t.Fatalf("got %v, want default-root", err)
	}
	if exists(filepath.Join(inside, "mirrors")) {
		t.Error("a folder was made in a default root")
	}
}

// TestADefineOfAProvisionedLeadIsRefused holds that a second define of a lead
// whose record is there is refused with ErrFirstStart and changes nothing: the
// caller skips provisioning for such a lead.
func TestADefineOfAProvisionedLeadIsRefused(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	mustDefine(t, w, cfg)
	before, err := os.ReadFile(scratchPaths(t, cfg).LeadRecord)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = defineLead(t, w, cfg)
	if !errors.Is(err, ErrFirstStart) {
		t.Fatalf("got %v, want ErrFirstStart", err)
	}
	after, _ := os.ReadFile(scratchPaths(t, cfg).LeadRecord)
	if string(before) != string(after) {
		t.Error("the record changed")
	}
}

func scratchPaths(t testing.TB, cfg leadcanary.Config) Paths {
	t.Helper()
	p, err := PathsOf(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestThePinMustMatchBeforeAnythingIsMade holds that a pin that is not the
// digest is refused before any folder or mirror is made, and that a pin that
// is the digest is accepted.
func TestThePinMustMatchBeforeAnythingIsMade(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	wrong, err := cfg.WithProfileDigest(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wrong.PinnedDigest(); !ok {
		t.Fatal("the config reports no pin")
	}
	_, _, err = defineLead(t, w, wrong)
	if !errors.Is(err, ErrDigestPin) {
		t.Fatalf("got %v, want ErrDigestPin", err)
	}
	for _, d := range []string{"mirrors", "leads", "scratch", "mailbox", "pub", "roles"} {
		if exists(filepath.Join(w.State, d)) {
			t.Errorf("%s was made before the pin was refused", d)
		}
	}
	if exists(w.Runtime) {
		t.Error("the runtime root was made")
	}
	// The right pin is accepted: the digest is the one the define computes
	// for these inputs, which a probe of the same code gives first.
	probe := w.Leads(t)[0]
	in, err := BuildInputs(probe, w.Config.Sandbox, hashHex(mustRead(t, w.Settings)))
	if err != nil {
		t.Fatal(err)
	}
	d, err := scratchDigest(probe, in)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := probe.WithProfileDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	if res := mustDefine(t, w, pinned); res.ProfileDigest != d {
		t.Errorf("the pinned define gave %q, want %q", res.ProfileDigest, d)
	}
}

func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRefusalsLeaveNothingBeforeTheMirror holds that a bad file to copy is
// refused before the mirror is made.
func TestRefusalsLeaveNothingBeforeTheMirror(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	if err := os.Chmod(w.Settings, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := defineLead(t, w, cfg); err == nil {
		t.Fatal("a writable settings file was accepted")
	}
	if exists(filepath.Join(w.State, "mirrors")) {
		t.Error("the mirrors folder was made before the files were checked")
	}
}

func TestTheSourceMustHoldTheBranch(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	cfg.Provision.UpstreamBranch = "develop"
	_, _, err := defineLead(t, w, cfg)
	if !errors.Is(err, ErrMirror) {
		t.Fatalf("got %v, want ErrMirror", err)
	}
	// The lead has not been made, and a state of none means the define can be
	// run again: the mirror was made empty, and is kept.
	if st, err := ScratchStateOf(cfg); err != nil || st != StateNone {
		t.Fatalf("state %v, %v", st, err)
	}
	cfg.Provision.UpstreamBranch = provisiontest.Branch
	mustDefine(t, w, cfg)
}

func TestAnExistingMirrorIsKept(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	// A first attempt that fails after the mirror was made (the branch is
	// wrong) leaves the mirror; the second one keeps it and imports the branch.
	bad := cfg
	bad.Provision.UpstreamBranch = "absent"
	if _, _, err := defineLead(t, w, bad); err == nil {
		t.Fatal("a branch the source lacks was accepted")
	}
	p := scratchPaths(t, cfg)
	if !exists(p.Mirror) {
		t.Fatal("the mirror is gone")
	}
	_, lines, err := defineLead(t, w, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "made the mirror") {
			t.Errorf("the mirror was made again: %q", lines)
		}
	}
}

// TestAnInterruptedDefineIsRecoverable holds F6: a define cut short leaves the
// lead incomplete, a second define says so and changes nothing, the owner's
// clean command removes exactly what was left, and the next define succeeds
// without any hand deletion.
func TestAnInterruptedDefineIsRecoverable(t *testing.T) {
	w := provisiontest.NewScratch(t)
	leads := w.Leads(t)
	cfg := leads[0]
	p := scratchPaths(t, cfg)

	// Cut the define short late, after the clone and the mount points: the
	// runtime root is made last, and its parent can be made unwritable.
	if os.Geteuid() == 0 {
		t.Skip("a folder mode does not stop root")
	}
	if err := os.Chmod(w.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.Chmod(w.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(restore)
	if _, _, err := defineLead(t, w, cfg); err == nil {
		t.Fatal("a define whose runtime root cannot be made succeeded")
	}
	restore()
	if !exists(p.Tree) || exists(p.RuntimeFolder) || exists(p.LeadRecord) {
		t.Fatal("the define was not cut short where the test means it to be")
	}
	// Another lead is defined in the same place after the failure.
	other := mustDefine(t, w, leads[1])
	// What the failed define left (and what was there) is an incomplete lead.
	if st, err := ScratchStateOf(cfg); err != nil || st != StateIncomplete {
		t.Fatalf("state %v, %v; want incomplete", st, err)
	}
	// A second define names the cause and the owner's command, and changes nothing.
	snapshot := listTree(t, w.State)
	_, _, err := defineLead(t, w, cfg)
	if !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "scratch-clean") || !strings.Contains(err.Error(), "--lead alpha") {
		t.Fatalf("got %v, want ErrIncomplete that names scratch-clean --lead alpha", err)
	}
	if after := listTree(t, w.State); !equalLists(snapshot, after) {
		t.Errorf("a refused define changed the state root:\nbefore %q\nafter  %q", snapshot, after)
	}

	// The clean command removes this lead's folders and only them.
	var removed []string
	if err := CleanScratch(cfg, Options{Getenv: w.Getenv(), Log: func(s string) { removed = append(removed, s) }}); err != nil {
		t.Fatalf("CleanScratch: %v", err)
	}
	if len(removed) == 0 {
		t.Error("CleanScratch said nothing")
	}
	for _, d := range []string{p.LeadDir, p.Tree, p.Mailbox, p.Published, p.RoleDir, p.RuntimeFolder, p.ScratchDir} {
		if exists(d) {
			t.Errorf("%s is still there", d)
		}
	}
	if !exists(p.Mirror) {
		t.Error("the mirror was removed")
	}
	// The other lead is intact and still done.
	po := scratchPaths(t, leads[1])
	if st, err := ScratchStateOf(leads[1]); err != nil || st != StateDone {
		t.Fatalf("the other lead: %v, %v", st, err)
	}
	if rec, err := ReadLeadRecord(leads[1]); err != nil || rec.ProfileDigest != other.ProfileDigest || !exists(po.Tree) {
		t.Errorf("the other lead was disturbed: %+v, %v", rec, err)
	}
	if st, _ := ScratchStateOf(cfg); st != StateNone {
		t.Fatalf("after the clean the state is %v", st)
	}
	// The next define succeeds.
	if res := mustDefine(t, w, cfg); res.ProfileDigest == "" {
		t.Error("no digest")
	}
	if st, _ := ScratchStateOf(cfg); st != StateDone {
		t.Fatalf("after the define the state is %v", st)
	}
	// Cleaning a done lead is refused, and so is a clean of one that was never made.
	if err := CleanScratch(cfg, Options{Getenv: w.Getenv()}); !errors.Is(err, ErrProvisioned) {
		t.Errorf("CleanScratch of a provisioned lead: %v", err)
	}
}

func TestCleanScratchOfALeadThatIsNotThereDoesNothing(t *testing.T) {
	w := provisiontest.NewScratch(t)
	var lines []string
	cfg := w.Leads(t)[0]
	if err := CleanScratch(cfg, Options{Getenv: w.Getenv(), Log: func(s string) { lines = append(lines, s) }}); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "nothing to remove") {
		t.Errorf("log %q", lines)
	}
}

func TestCleanScratchRefusesALeadThatHasStarted(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	p := scratchPaths(t, cfg)
	// An incomplete lead whose holder's state folder holds a mark of a start.
	for _, d := range []string{p.LeadsDir, p.LeadDir, p.Holder} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
	}
	provisiontest.WriteFile(t, filepath.Join(p.Holder, "lead-has-run"), nil, 0o600)
	if st, _ := ScratchStateOf(cfg); st != StateIncomplete {
		t.Fatalf("state %v", st)
	}
	err := CleanScratch(cfg, Options{Getenv: w.Getenv()})
	if !errors.Is(err, ErrFirstStart) {
		t.Fatalf("got %v, want ErrFirstStart", err)
	}
	if !exists(filepath.Join(p.Holder, "lead-has-run")) {
		t.Error("the mark was removed")
	}
}

func TestCleanScratchLeavesALinkAlone(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	p := scratchPaths(t, cfg)
	elsewhere := t.TempDir()
	provisiontest.WriteFile(t, filepath.Join(elsewhere, "keep"), nil, 0o600)
	if err := os.Mkdir(p.LeadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, p.LeadDir); err != nil {
		t.Fatal(err)
	}
	err := CleanScratch(cfg, Options{Getenv: w.Getenv()})
	if err == nil {
		t.Fatal("a link where the lead's folder belongs was removed")
	}
	if !exists(filepath.Join(elsewhere, "keep")) {
		t.Error("what the link led to was removed")
	}
}

func TestCleanScratchNeedsTheOwnersRootMarker(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	p := scratchPaths(t, cfg)
	if err := os.MkdirAll(p.LeadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(w.State, canaryroot.ScratchMarkerName)); err != nil {
		t.Fatal(err)
	}
	var re *canaryroot.RefusedError
	if err := CleanScratch(cfg, Options{Getenv: w.Getenv()}); !errors.As(err, &re) {
		t.Fatalf("got %v, want a root refusal", err)
	}
	if !exists(p.LeadDir) {
		t.Error("a folder was removed in a place without the marker")
	}
}

func TestReadLeadRecordRefusesAnAlteredRecord(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	mustDefine(t, w, cfg)
	p := scratchPaths(t, cfg)
	good := mustRead(t, p.LeadRecord)
	digest := strings.TrimSpace(strings.Split(strings.Split(string(good), `"profile_digest":"`)[1], `"`)[0])
	for name, content := range map[string]string{
		"another project":   strings.Replace(string(good), "scratch-alpha", "scratch-beta", 1),
		"another marker":    strings.Replace(string(good), "ataniq-scratch-lead", "ataniq-canary-root", 1),
		"an extra key":      strings.Replace(string(good), "}\n", `,"x":1}`+"\n", 1),
		"no line break":     strings.TrimSuffix(string(good), "\n"),
		"a short digest":    strings.Replace(string(good), digest, digest[:10], 1),
		"an upper digest":   strings.Replace(string(good), digest, strings.ToUpper(digest), 1),
		"not json":          "scratch\n",
		"empty":             "",
		"too long":          string(good) + strings.Repeat(" ", 2048),
		"a second document": string(good) + string(good),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.Remove(p.LeadRecord); err != nil {
				t.Fatal(err)
			}
			provisiontest.WriteFile(t, p.LeadRecord, []byte(content), 0o600)
			if _, err := ReadLeadRecord(cfg); !errors.Is(err, ErrRecord) {
				t.Fatalf("got %v, want ErrRecord", err)
			}
			// A record that is not valid is never "done".
			if st, err := ScratchStateOf(cfg); err == nil || st == StateDone {
				t.Fatalf("state %v, %v", st, err)
			}
		})
	}
	t.Run("mode", func(t *testing.T) {
		if err := os.Remove(p.LeadRecord); err != nil {
			t.Fatal(err)
		}
		provisiontest.WriteFile(t, p.LeadRecord, good, 0o640)
		if _, err := ReadLeadRecord(cfg); !errors.Is(err, ErrRecord) {
			t.Fatalf("got %v, want ErrRecord", err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if err := os.Remove(p.LeadRecord); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLeadRecord(cfg); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("got %v, want not-exist", err)
		}
	})
}

// TestWriteInputsIsExclusiveAndExact holds the rules of the inputs file: the
// exact path, mode 0600, nothing replaced.
func TestWriteInputsIsExclusiveAndExact(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	p := scratchPaths(t, cfg)
	sha := hashHex(mustRead(t, w.Settings))
	if err := os.MkdirAll(p.ScratchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteInputs(cfg, w.Config.Sandbox, sha); err != nil {
		t.Fatal(err)
	}
	if fi := lstat(t, p.SandboxInputs); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := sandboxstart.ReadInputs(p.SandboxInputs); err != nil {
		t.Fatalf("the file does not read back: %v", err)
	}
	if err := WriteInputs(cfg, w.Config.Sandbox, sha); err == nil {
		t.Error("a second write replaced the file")
	}
	// Another path is refused.
	other := cfg
	other.Holder.SandboxInputs = filepath.Join(w.Dir, "elsewhere.json")
	if err := WriteInputs(other, w.Config.Sandbox, sha); err == nil {
		t.Error("an inputs file outside the lead's folder was written")
	}
	// A symbolic link at the name is not followed.
	if err := os.Remove(p.SandboxInputs); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(w.Dir, "target")
	if err := os.Symlink(target, p.SandboxInputs); err != nil {
		t.Fatal(err)
	}
	if err := WriteInputs(cfg, w.Config.Sandbox, sha); err == nil {
		t.Error("wrote through a symbolic link")
	}
	if exists(target) {
		t.Error("the link's target was created")
	}
}

func TestBuildInputsHoldsTheSandboxValues(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	sb := w.Config.Sandbox
	sb.Terminal = leadcanary.ScratchTerminal{Term: "xterm-256color", ColorTerm: "truecolor", Lang: "C.UTF-8", TZ: "UTC"}
	in, err := BuildInputs(cfg, sb, hashHex([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	p := scratchPaths(t, cfg)
	if !in.Canary || in.Project != "scratch-alpha" || in.LayoutRole != "scratch-alpha-lead" ||
		in.Executables.Pasta != sb.Pasta || in.Executables.Bwrap != sb.Bwrap || in.Executables.Probe != sb.Probe ||
		in.Sources.AgentInstall != sb.AgentInstall || in.Sources.ManagedSettings != p.ManagedSettings ||
		in.Sources.ManagedMarker != sb.ManagedMarker || in.Sources.QargiRecordsInside != RecordsInside ||
		in.ProviderHost != sb.ProviderHost || in.TimeLimitSeconds != 60 ||
		in.Terminal.Term != "xterm-256color" || in.Terminal.TZ != "UTC" {
		t.Errorf("inputs %+v", in)
	}
	bad := sb
	bad.NeverMounted = nil
	if _, err := BuildInputs(cfg, bad, hashHex(nil)); err == nil {
		t.Error("sandbox values that do not validate were accepted")
	}
}

// TestTimeLimitBoundsAreTheInputsBounds holds that the scratch config's bounds
// for the time limit are the inputs file's own.
func TestTimeLimitBoundsAreTheInputsBounds(t *testing.T) {
	if leadcanary.MinTimeLimitSeconds != sandboxstart.MinTimeLimitSeconds || leadcanary.MaxTimeLimitSeconds != sandboxstart.MaxTimeLimitSeconds {
		t.Fatal("the scratch config's time limit bounds are not the sandbox inputs' bounds")
	}
}

// listTree lists the paths below dir with their type, mode and size, sorted by
// the walk, to compare two states.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, p+" "+fi.Mode().String()+" "+string(rune('0'+fi.Size()%10)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equalLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
