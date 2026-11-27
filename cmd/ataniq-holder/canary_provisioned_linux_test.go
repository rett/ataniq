//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
)

// The tests of this file start the holder from a tree that ataniq-canary
// provision really made, and from the command line the daemon really builds
// (leadcanary.HolderCommand), and make no folder, marker or file of the layout
// by hand. The rigs of the other tests make the folders they need themselves,
// which is how the first attended run could find a folder that no production
// code made (the folder of the role lock) with every test green. A folder or a
// file that the holder expects and provisioning does not make fails a test
// here.

// provisionedRig is a canary rig over a tree that provisioning made. Only the
// seams of a holder test are fake: the start sequence's system, the launcher
// and the proc root; the folders, the marker, the clone and the paths on the
// command line are the production code's.
type provisionedRig struct {
	*canaryRig
	world *provisiontest.World
	paths provision.Paths
	cfg   leadcanary.Config
}

func newProvisionedRig(t *testing.T) *provisionedRig {
	t.Helper()
	w := provisiontest.New(t)
	initInstanceIdentity(t, w.State)
	var log []string
	if err := provision.Provision(context.Background(), w.Cfg, provision.Options{
		Getenv: w.Getenv(), Log: func(s string) { log = append(log, s) },
	}); err != nil {
		t.Fatalf("provisioning: %v\n%q", err, log)
	}
	p, err := provision.PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The sandbox inputs file is the run kit's, written from the config and the
	// paths provisioning reports. The fake system never looks at its sources.
	in := sampleInputs(w.Dir)
	in.StateRoot, in.RuntimeRoot = w.State, w.Runtime
	in.Runtime, in.RuntimeVersion = w.Cfg.Agent.Runtime, w.Cfg.Agent.RuntimeVersion
	in.Sources.ManagedSettings, in.Sources.LeadSkills, in.Sources.InstructionFile = p.ManagedSettings, p.LeadSkills, p.InstructionFile
	in.Sources.GitIdentityDir, in.Sources.QargiInstall = p.GitIdentityDir, p.QargiInstall
	in.Sources.AssignmentDir, in.Sources.ProjectionDir = p.AssignmentDir, p.ProjectionDir
	inputs := w.Cfg.Holder.SandboxInputs
	if err := os.Mkdir(filepath.Dir(inputs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := provision.CheckInputs(w.Cfg, writeInputsFile(t, inputs, in)); err != nil {
		t.Fatalf("the inputs file does not match the provisioned paths: %v", err)
	}

	// The rig's own folders are a temporary root of its own: its state, runtime
	// and tree are replaced by the provisioned ones, and the daemon's lock it
	// holds is the one on the command line.
	r := newRig(t)
	r.state, r.tree, r.runtime, r.roleLock = p.Holder, p.Tree, p.RuntimeFolder, p.RoleLock
	c := &canaryRig{
		rig: r, base: w.Dir, stateRoot: w.State, runRoot: w.Runtime,
		inputsPath: inputs, in: in, sys: newFakeSystem(), script: "wait",
		getenv: w.Getenv(), interval: 10 * time.Millisecond,
	}
	return &provisionedRig{canaryRig: c, world: w, paths: p, cfg: w.Cfg}
}

func writeInputsFile(t *testing.T, path string, in sandboxstart.Inputs) string {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// holderArgs is the argument vector of the holder as the daemon builds it for
// the config's role and the given pin, without the program's name.
func (p *provisionedRig) holderArgs(digest string) []string {
	p.t.Helper()
	cfg := p.cfg
	cfg.Holder.ProfileDigest = digest
	hc, err := leadcanary.NewHolderCommand(cfg, p.daemonLock)
	if err != nil {
		p.t.Fatal(err)
	}
	_, unit, err := leadcanary.Names(cfg.Role)
	if err != nil {
		p.t.Fatal(err)
	}
	entry := leadregistry.Entry{Role: cfg.Role, Epoch: testEpoch}
	intent := leadregistry.Intent{ID: "intent-0123456789abcdef", Epoch: testEpoch, UnitName: unit}
	argv, workdir, _, err := hc.Build(cfg.Role, entry, intent)
	if err != nil {
		p.t.Fatal(err)
	}
	if workdir != p.paths.Holder {
		p.t.Fatalf("the unit's working directory is %s, provisioning made the holder folder %s", workdir, p.paths.Holder)
	}
	return argv[1:]
}

// The holder's refusals that write nothing, then the role lock, from a tree
// that provisioning made and the command line the daemon builds. This is the
// start that failed in the first attended run, at "--role-lock cannot be taken
// as the role lock: no such file or directory", before anything else.
func TestHolderPreLockChecksAndTheRoleLockInAProvisionedTree(t *testing.T) {
	p := newProvisionedRig(t)
	args := p.holderArgs(strings.Repeat("0", 64))

	cfg, err := parse(args)
	if err != nil {
		t.Fatalf("the daemon's command line does not parse: %v", err)
	}
	if cfg.roleLock != p.paths.RoleLock {
		t.Fatalf("--role-lock is %s, provisioning's role lock is %s", cfg.roleLock, p.paths.RoleLock)
	}
	var out bytes.Buffer
	if _, code := vetMode(cfg, p.deps(), holderOut{&out}); code != exitOK {
		t.Fatalf("the checks before the role lock refuse (exit %d): %s", code, out.String())
	}
	// Nothing was written by the checks: the lock is not made yet.
	if _, err := os.Lstat(p.paths.RoleLock); err == nil {
		t.Fatal("the checks before the role lock made the lock file")
	}
	lock, err := session.TakeRoleLock(cfg.roleLock)
	if err != nil {
		t.Fatalf("the role lock cannot be taken in the provisioned tree: %v", err)
	}
	defer lock.Close()
	if _, err := session.TakeRoleLock(cfg.roleLock); !errors.Is(err, session.ErrRoleLocked) {
		t.Errorf("a second holder: %v, want ErrRoleLocked", err)
	}
}

// The whole start, over the fake start sequence, from the same tree: the
// holder gets past every check and write that comes before the sequence (the
// folders of its state and runtime, the tree, the role lock, the event log and
// the state file), serves with the role lock held, and releases it at the stop.
func TestHolderStartsAndStopsInAProvisionedTree(t *testing.T) {
	p := newProvisionedRig(t)
	// The first start, pinned to nothing, is refused at the step that names
	// the digest of the plan; the folders were all there for it to get so far.
	if code := p.runNow(p.holderArgs(strings.Repeat("0", 64))); code != exitSandbox {
		t.Fatalf("the probing start exited %d; stderr: %s", code, p.errbuf.String())
	}
	m := pinnedDigest.FindStringSubmatch(p.errbuf.String())
	if m == nil {
		t.Fatalf("the probing start does not name the digest: %s", p.errbuf.String())
	}
	if n := len(p.launches()); n != 0 {
		t.Fatalf("the probing start launched %d times", n)
	}
	if _, err := os.Lstat(p.paths.RoleLock); err != nil {
		t.Fatalf("the probing start did not get as far as the role lock: %v", err)
	}
	// What it left in the holder's folder is cleared, so that the next start is
	// a first one.
	p.clearHolderFolder()
	p.errbuf, p.sys = &syncBuf{}, newFakeSystem()

	p.start(p.holderArgs(m[1]))
	p.waitReady()
	if !p.lockHeld() {
		t.Error("the role lock is not held while the holder serves")
	}
	p.stopAndWait()
	if !p.lockFree() {
		t.Error("the role lock is still held after the stop")
	}
}

// lockHeld says the role lock is held: it cannot be taken.
func (c *canaryRig) lockHeld() bool {
	f, err := session.TakeRoleLock(c.roleLock)
	if err == nil {
		f.Close()
		return false
	}
	return errors.Is(err, session.ErrRoleLocked)
}

// ---- the real file-system steps of the start sequence ----

// fsSystem is the fake system with the steps that touch the file system
// replaced by the real ones (sandboxstart.NewSystem): the host check, the
// mount point work, the bound folder, the sources and the report channel. What
// stays fake is what asks the host about itself: the tools' versions, the
// resolv.conf, the identities and the outside checks.
type fsSystem struct {
	*fakeSystem
	real sandboxstart.System
}

func (s *fsSystem) CheckHost(in sandboxstart.Inputs) ([]sandboxstart.ExeRecord, error) {
	return s.real.CheckHost(in)
}
func (s *fsSystem) CheckOwnClone(in profile.Inputs, uid uint32) (mountpoints.Report, error) {
	return s.real.CheckOwnClone(in, uid)
}
func (s *fsSystem) PrepareMountPoints(in profile.Inputs) error { return s.real.PrepareMountPoints(in) }
func (s *fsSystem) CheckMountPoints(in profile.Inputs) (mountpoints.Report, error) {
	return s.real.CheckMountPoints(in)
}
func (s *fsSystem) MakeBound(runtimeRoot, layoutRole string) (sandboxstart.Bound, error) {
	return s.real.MakeBound(runtimeRoot, layoutRole)
}
func (s *fsSystem) ResolveSources(in profile.Inputs) (mountpoints.Resolution, error) {
	return s.real.ResolveSources(in)
}
func (s *fsSystem) HostHas(p string) bool { return s.real.HostHas(p) }
func (s *fsSystem) OpenReport(runtimeRoot, layoutRole string) (sandboxstart.ReportChannel, error) {
	return s.real.OpenReport(runtimeRoot, layoutRole)
}
func (s *fsSystem) CheckBindSources(rows []mountpoints.ResolvedSource) mountpoints.Report {
	return s.real.CheckBindSources(rows)
}

// The start sequence's steps that touch the file system run over the real
// System (see fsSystem) in the tree provisioning made, up to the launch: the
// host check, the own-clone check, the mount points and their walks, the bound
// folder, the sources, the report channel, the write of the spec, the second
// walk and the check of the bind sources. The launch is the one thing refused,
// so that nothing starts; a folder or a file that a step expects and
// provisioning did not make refuses the start at that step instead.
func TestHolderFileSystemStepsInAProvisionedTree(t *testing.T) {
	p := newProvisionedRig(t)
	// What the run kit's inputs name that provisioning does not make: the three
	// executables and the agent's install folder.
	for _, d := range []string{filepath.Join(p.world.Dir, "bin"), p.in.Sources.AgentInstall} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{p.in.Executables.Pasta, p.in.Executables.Bwrap, p.in.Executables.Probe} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := os.ReadFile(p.paths.ManagedSettings)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(settings)
	p.in.Sources.ManagedSettingsSHA256 = hex.EncodeToString(sum[:])
	writeInputsFile(t, p.inputsPath, p.in)

	start := func(digest string) int {
		d := p.canaryRig.deps()
		d.canary.newSystem = func(cfg sandboxstart.SystemConfig) (sandboxstart.System, error) {
			real, err := sandboxstart.NewSystem(cfg)
			if err != nil {
				return nil, err
			}
			p.sys.setLock(cfg.RoleLock)
			return &fsSystem{fakeSystem: p.sys, real: real}, nil
		}
		return run(p.holderArgs(digest), d)
	}
	// A start pinned to nothing is refused at verify-plan, which comes after
	// most of the real steps, and names the digest of the plan.
	if code := start(strings.Repeat("0", 64)); code != exitSandbox {
		t.Fatalf("the probing start exited %d; stderr: %s", code, p.errbuf.String())
	}
	m := pinnedDigest.FindStringSubmatch(p.errbuf.String())
	if m == nil {
		t.Fatalf("the probing start does not name the digest: %s", p.errbuf.String())
	}
	p.errbuf = &syncBuf{}
	// The pinned start goes on to the launch, which the seam refuses.
	p.clearHolderFolder()
	p.startErr = syscall.ENOENT
	if code := start(m[1]); code != exitSandbox || !strings.Contains(p.errbuf.String(), "at the step launch") {
		t.Fatalf("the pinned start exited %d, want a refusal at the step launch; stderr: %s", code, p.errbuf.String())
	}
	if n := len(p.launches()); n != 1 {
		t.Errorf("the launcher was reached %d times, want once", n)
	}
	// Whatever the sequence made in the runtime folder is gone with the start.
	if ents, err := os.ReadDir(p.paths.RuntimeFolder); err != nil || len(ents) != 0 {
		t.Errorf("the runtime folder holds %v after the refused start (%v)", ents, err)
	}
}

// clearHolderFolder removes what a start left in the holder's state folder, as
// digest() does, so that the next start is a first one.
func (p *provisionedRig) clearHolderFolder() {
	p.t.Helper()
	ents, err := os.ReadDir(p.paths.Holder)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(p.paths.Holder, e.Name())); err != nil {
			p.t.Fatal(err)
		}
	}
}
