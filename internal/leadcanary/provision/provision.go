//go:build linux && ataniq_canary

package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/sys/unix"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
	"ataniq/internal/mirror"
)

// Options are what a caller of the entry points of this package may give.
type Options struct {
	// Getenv reads the environment for the check of the default roots. Nil
	// means os.Getenv. The check works out the account's defaults whatever
	// this says.
	Getenv func(string) string
	// Log receives one line for each folder or file made or kept, and for
	// the clone and the marker. It never receives the content of a file.
	Log func(string)
}

func (o Options) getenv() func(string) string {
	if o.Getenv != nil {
		return o.Getenv
	}
	return os.Getenv
}

// Names of the files the holder leaves in its state folder, and of the folder's
// neighbours, that show a first start has happened. The holder writes
// "holder.json" while it runs, "final.json" when it ends and "lead-has-run"
// before it first starts the sandbox.
var firstStartMarks = []string{"holder.json", "final.json", "lead-has-run"}

// ErrFirstStart is wrapped when the lead's folders show that the lead has
// started already: the marker is there, or the holder's state folder holds
// something.
var ErrFirstStart = errors.New("provision: the lead has been started already, or this root was provisioned")

// ErrNotEmpty is wrapped when a folder that provisioning would keep holds
// something. The message names the folder and not what it holds.
var ErrNotEmpty = errors.New("provision: a folder that is there already is not empty")

// ErrMirror is wrapped when the mirror is not usable.
var ErrMirror = errors.New("provision: the mirror is not usable")

// gitMetaTmpSuffix is what the mount point work adds to the git metadata
// folder's name while it builds the folder. The mountpoints package keeps the
// constant to itself and names the folder, gitmeta.tmp, in its package comment.
const gitMetaTmpSuffix = ".tmp"

// run is one provisioning.
type run struct {
	cfg  leadcanary.Config
	opts Options
	p    Paths
	say  func(string)

	state *area // the state root

	settings []byte
	hook     []byte
	replay   replayScreens
	mirror   *mirror.Mirror
}

// Provision makes the folders of the one canary lead and writes the marker
// last. See the package comment for what it makes and refuses.
//
// Nothing is made until every check that needs no write has passed: the roots,
// the files it copies, the mirror, the marker and the holder's state folder
// of the state root, and every path it will make. A failure after that leaves
// what was made so far, and no marker. A second run refuses what the first
// left, so remove it before running again: the lead's folder
// (<state root>/leads/<layout role>), its mailbox and published folders
// (<state root>/mailbox/<layout role>, <state root>/pub/<project>) and the
// runtime root. The mirror stays: it is in the state root, provisioning needs
// it and does not make it, so the state root itself is not removed.
func Provision(ctx context.Context, cfg leadcanary.Config, opts Options) error {
	r := &run{cfg: cfg, opts: opts, say: func(s string) {
		if opts.Log != nil {
			opts.Log(s)
		}
	}}
	if err := cfg.Validate(); err != nil {
		return err
	}
	// First, before any file is looked at: the roots must not be, hold or lie
	// in the account's default state or runtime folders. WriteMarker does not
	// look where the root is.
	if err := canaryroot.CheckRoots(cfg.StateRoot, cfg.RuntimeRoot, opts.getenv()); err != nil {
		return err
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return err
	}
	r.p = p
	if cfg.Provision.Mirror != p.Mirror {
		return fmt.Errorf("%w: provision.mirror must be %s, the mirror of the layout", ErrMirror, p.Mirror)
	}
	if err := r.loadSources(); err != nil {
		return err
	}
	if err := r.checkMirror(ctx); err != nil {
		return err
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	r.state = &area{root: root, base: p.StateRoot, log: r.say}
	if err := r.preflight(); err != nil {
		return err
	}
	return r.execute(ctx)
}

// loadSources reads, and checks, every file that is copied, before anything
// is made.
func (r *run) loadSources() error {
	var err error
	pc := r.cfg.Provision
	if r.settings, err = readTrusted("provision.managed_settings", pc.ManagedSettings, MaxManagedSettingsBytes, false); err != nil {
		return err
	}
	if len(r.settings) == 0 {
		return errors.New("provision.managed_settings: the file is empty")
	}
	if r.hook, err = readTrusted("provision.guard_hook", pc.GuardHook, MaxGuardHookBytes, true); err != nil {
		return err
	}
	if len(r.hook) == 0 {
		return errors.New("provision.guard_hook: the file is empty")
	}
	r.replay, err = readReplay(pc.ReplayScreens)
	return err
}

// checkMirror opens the mirror as the exchange mirror it must be and looks for
// the branch the tree starts from. It reads and writes nothing.
func (r *run) checkMirror(ctx context.Context) error {
	git, err := gitenv.New(GitPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMirror, err)
	}
	m, err := mirror.Open(ctx, git, r.p.Mirror)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMirror, err)
	}
	ref, err := mirror.UpstreamRef(r.cfg.Provision.UpstreamBranch)
	if err != nil {
		return fmt.Errorf("%w: provision.upstream_branch: %w", ErrMirror, err)
	}
	if _, err := m.Resolve(ctx, ref); err != nil {
		return fmt.Errorf("%w: the mirror has no upstream branch %q: %w", ErrMirror, r.cfg.Provision.UpstreamBranch, err)
	}
	r.mirror = m
	return nil
}

// stateFolders are the folders made in the state root, top down, as paths
// relative to it. The mirrors folder is made by the mirror's own creation and
// only checked here.
func (r *run) stateFolders() []string {
	rel := r.p.stateRel
	return []string{
		rel(r.p.LeadsDir), rel(r.p.LeadDir), rel(r.p.Holder), rel(r.p.Home),
		rel(r.p.ConfigRoot), rel(r.p.ConfigDir), rel(r.p.Tree),
		rel(r.p.Records), rel(r.p.Requests), rel(r.p.Mail),
		rel(r.p.LeadSkills), rel(r.p.GitIdentityDir), rel(r.p.QargiInstall),
		rel(r.p.AssignmentDir), rel(r.p.ProjectionDir),
		rel(r.p.MailboxesDir), rel(r.p.Mailbox), rel(r.p.PublishedDir), rel(r.p.Published),
		rel(r.p.MirrorsDir), rel(r.p.ReplayDir),
	}
}

// sharedParent reports that the folder rel, of stateFolders, is one that holds
// other things than this lead's by design, or holds the lead's other folders:
// the leads folder, the lead's own folder, the mailboxes folder, the published
// folder and the mirrors folder. Every other folder that is there before the
// run must be empty.
func (r *run) sharedParent(rel string) bool {
	for _, abs := range []string{r.p.LeadsDir, r.p.LeadDir, r.p.MailboxesDir, r.p.PublishedDir, r.p.MirrorsDir} {
		if rel == r.p.stateRel(abs) {
			return true
		}
	}
	return false
}

// stateFile is a file made in the state root.
type stateFile struct {
	rel  string
	data []byte
	mode fs.FileMode
}

// stateFiles are the files made in the state root, after their folders. The
// copies of the replay screens come last.
func (r *run) stateFiles() []stateFile {
	rel := r.p.stateRel
	files := []stateFile{
		{rel(r.p.GitIdentityFile), identityConfig(r.cfg.Provision.GitName, r.cfg.Provision.GitEmail), 0o600},
		{rel(r.p.InstructionFile), nil, 0o600},
		{rel(r.p.ManagedSettings), r.settings, 0o400},
		{rel(r.p.GuardHook), r.hook, 0o500},
	}
	for i, name := range r.replay.names {
		files = append(files, stateFile{rel(path.Join(r.p.ReplayDir, name)), r.replay.data[i], 0o600})
	}
	return files
}

// preflight looks at the whole plan without writing: the state root, the
// marker, the holder's state folder, the runtime root, and every path that
// will be made.
func (r *run) preflight() error {
	info, err := r.state.root.Lstat(".")
	if err != nil {
		return fmt.Errorf("state root %s: %w", r.p.StateRoot, err)
	}
	if why := checkFolder(info); why != "" {
		return fmt.Errorf("state root %s: %s", r.p.StateRoot, why)
	}
	// The marker: provisioning ran already.
	if _, err := r.state.root.Lstat(canaryroot.MarkerName); err == nil {
		return fmt.Errorf("%w: %s is there", ErrFirstStart, r.p.Marker)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", r.p.Marker, err)
	}
	// The holder's state folder: a first start happened.
	if name, err := r.state.empty(r.p.stateRel(r.p.Holder)); err != nil {
		return err
	} else if name != "" {
		return fmt.Errorf("%w: the holder's state folder %s holds %q", ErrFirstStart, r.p.Holder, markName(name))
	}
	// Every folder: absent, or a folder as provisioning makes it. Those that
	// are there are kept, and looked into below.
	var kept []string
	for _, rel := range r.stateFolders() {
		ok, err := r.state.lookFolder(rel)
		if err != nil {
			return err
		}
		if ok && !r.sharedParent(rel) {
			kept = append(kept, rel)
		}
	}
	// The mirrors folder is the mirror's parent: it exists, or the mirror
	// check would have failed.
	if ok, _ := r.state.lookFolder(r.p.stateRel(r.p.MirrorsDir)); !ok {
		return fmt.Errorf("%w: %s does not exist", ErrMirror, r.p.MirrorsDir)
	}
	for _, f := range r.stateFiles() {
		if err := r.state.lookAbsent(f.rel); err != nil {
			return err
		}
	}
	// The replay switch is written by ReplayOn alone, after the marker: one
	// that is there already would start the first epoch in replay mode.
	if err := r.state.lookAbsent(r.p.stateRel(r.p.ReplaySwitch)); err != nil {
		return err
	}
	// The git metadata folder and the temporary name it is built under, which
	// the mount point work makes after the clone: nothing may be at either,
	// a symbolic link included. The lead's folder is not required to be
	// empty, so this look is what refuses an old copy.
	for _, abs := range []string{r.p.GitMeta, r.p.GitMeta + gitMetaTmpSuffix} {
		if err := r.state.lookAbsent(r.p.stateRel(abs)); err != nil {
			return err
		}
	}
	// The mount points in the home and the config folder, which the mount
	// point work makes: a symbolic link there is refused now. Those in the
	// tree cannot exist, the tree being empty.
	in := profileInputs(r.cfg, r.p, hashHex(r.settings))
	pts, err := mountpoints.List(in)
	if err != nil {
		return fmt.Errorf("the profile inputs of the layout: %w", err)
	}
	for _, pt := range pts {
		if pt.Root == mountpoints.RootTree {
			continue
		}
		info, err := r.state.root.Lstat(r.p.stateRel(pt.HostPath))
		switch {
		case err == nil && info.IsSymlink():
			return fmt.Errorf("%s: is a symbolic link", pt.HostPath)
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: %w", pt.HostPath, err)
		}
	}
	// A folder that is kept must be empty: what it holds would be bound into
	// the sandbox as if provisioning had made it (a leftover skill, a config
	// file, a screen). This comes after every look above, so that a symbolic
	// link or a file at a made path is named as what it is. The tree is among
	// them: it is cloned into.
	for _, rel := range kept {
		if name, err := r.state.empty(rel); err != nil {
			return err
		} else if name != "" {
			return fmt.Errorf("%w: %s", ErrNotEmpty, r.state.at(rel))
		}
	}
	return r.preflightRuntime()
}

// markName gives the name a folder holds, for a message: the known marks are
// named as they are, any other entry is only said to be there.
func markName(name string) string {
	for _, m := range firstStartMarks {
		if name == m {
			return name
		}
	}
	return "an entry"
}

// preflightRuntime looks at the runtime root and the holder's runtime folder:
// each is absent, or a folder as provisioning makes it, and the runtime folder
// holds nothing.
func (r *run) preflightRuntime() error {
	parent, err := safefile.OpenRootNoLinks(filepath.Dir(r.p.RuntimeRoot))
	if err != nil {
		return fmt.Errorf("the folder of the runtime root %s: %w", r.p.RuntimeRoot, err)
	}
	defer parent.Close()
	pa := &area{root: parent, base: filepath.Dir(r.p.RuntimeRoot)}
	exists, err := pa.lookFolder(filepath.Base(r.p.RuntimeRoot))
	if err != nil || !exists {
		return err
	}
	root, err := parent.SubRoot(filepath.Base(r.p.RuntimeRoot))
	if err != nil {
		return fmt.Errorf("runtime root %s: %w", r.p.RuntimeRoot, err)
	}
	defer root.Close()
	ra := &area{root: root, base: r.p.RuntimeRoot}
	for _, rel := range []string{"leads", "leads/" + r.p.Layout.Role} {
		if _, err := ra.lookFolder(rel); err != nil {
			return err
		}
	}
	if name, err := ra.empty("leads/" + r.p.Layout.Role); err != nil {
		return err
	} else if name != "" {
		return fmt.Errorf("%w: the holder's runtime folder %s holds an entry", ErrFirstStart, r.p.RuntimeFolder)
	}
	return nil
}

// execute makes everything. The marker is the last act.
func (r *run) execute(ctx context.Context) error {
	for _, rel := range r.stateFolders() {
		if err := r.state.dir(rel); err != nil {
			return err
		}
	}
	if err := r.cloneTree(ctx); err != nil {
		return err
	}
	for _, f := range r.stateFiles() {
		if err := r.state.file(f.rel, f.data, f.mode); err != nil {
			return err
		}
	}
	in := profileInputs(r.cfg, r.p, hashHex(r.settings))
	if err := mountpoints.Prepare(in, mountpoints.PrepareOptions{NoLeadHasRun: true}); err != nil {
		return fmt.Errorf("the mount points: %w", err)
	}
	r.say("made the mount points and the git metadata folder " + r.p.GitMeta)
	if err := r.checkResult(in); err != nil {
		return err
	}
	if err := r.makeRuntime(); err != nil {
		return err
	}
	return r.writeMarker()
}

// cloneTree clones the mirror's branch into the tree, by the handle of the
// tree, and makes the .git/info folder that a clone without a template lacks.
func (r *run) cloneTree(ctx context.Context) error {
	rel := r.p.stateRel(r.p.Tree)
	if name, err := r.state.empty(rel); err != nil {
		return err
	} else if name != "" {
		return fmt.Errorf("%s: is not empty (it holds %q)", r.p.Tree, name)
	}
	h, err := r.state.root.OpenDirHandle(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", r.p.Tree, err)
	}
	defer h.Close()
	if err := cloneInto(ctx, h, r.p.Mirror, r.cfg.Provision.UpstreamBranch, insideMirror(r.p.Layout.Project)); err != nil {
		return fmt.Errorf("the clone into %s: %w", r.p.Tree, err)
	}
	tree, err := r.state.root.SubRoot(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", r.p.Tree, err)
	}
	defer tree.Close()
	ta := &area{root: tree, base: r.p.Tree, log: r.say}
	if err := ta.dir(".git/info"); err != nil {
		return err
	}
	r.say("cloned " + r.p.Mirror + " into " + r.p.Tree)
	return nil
}

// checkResult runs, on what was made, the checks that the holder runs before
// every start: the walk over the mount points and the git metadata folder, and
// the check that the tree is the lead's own clone and the folders of the lead
// are the caller's alone. Anything they find refuses the run, before the marker.
func (r *run) checkResult(in profile.Inputs) error {
	rep, err := mountpoints.Check(in)
	if err != nil {
		return fmt.Errorf("the mount point check: %w", err)
	}
	if err := rep.Err(); err != nil {
		return fmt.Errorf("the mount point check: %w", err)
	}
	own, err := mountpoints.CheckOwnClone(in, uint32(unix.Geteuid()))
	if err != nil {
		return fmt.Errorf("the own-clone check: %w", err)
	}
	if err := own.Err(); err != nil {
		return fmt.Errorf("the own-clone check: %w", err)
	}
	r.say("checked the mount points and the clone")
	return nil
}

// makeRuntime makes the runtime root, if it is not there, and the holder's
// runtime folder in it.
func (r *run) makeRuntime() error {
	parent, err := safefile.OpenRootNoLinks(filepath.Dir(r.p.RuntimeRoot))
	if err != nil {
		return fmt.Errorf("the folder of the runtime root %s: %w", r.p.RuntimeRoot, err)
	}
	defer parent.Close()
	pa := &area{root: parent, base: filepath.Dir(r.p.RuntimeRoot), log: r.say}
	if err := pa.dir(filepath.Base(r.p.RuntimeRoot)); err != nil {
		return err
	}
	root, err := parent.SubRoot(filepath.Base(r.p.RuntimeRoot))
	if err != nil {
		return fmt.Errorf("runtime root %s: %w", r.p.RuntimeRoot, err)
	}
	defer root.Close()
	ra := &area{root: root, base: r.p.RuntimeRoot, log: r.say}
	for _, rel := range []string{"leads", "leads/" + r.p.Layout.Role} {
		if err := ra.dir(rel); err != nil {
			return err
		}
	}
	return nil
}

// writeMarker writes the marker, the last act. The state root must still be
// the folder that was opened: WriteMarker names it by path.
func (r *run) writeMarker() error {
	info, err := r.state.root.Lstat(".")
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(r.p.StateRoot, &st); err != nil {
		return fmt.Errorf("state root %s: %w", r.p.StateRoot, err)
	}
	if uint64(st.Dev) != info.Dev || uint64(st.Ino) != info.Ino {
		return fmt.Errorf("state root %s: is not the folder that was provisioned", r.p.StateRoot)
	}
	if err := canaryroot.WriteMarker(r.p.StateRoot, r.p.Layout.Project); err != nil {
		return err
	}
	r.say("made file " + r.p.Marker)
	return nil
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
