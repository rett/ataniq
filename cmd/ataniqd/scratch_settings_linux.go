//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
)

// This file is run --owner-settings DIR and run --check, in a build with both
// tags (ataniq_ownerchannel and ataniq_canary, make scratch-seat-build): the
// scratch daemon with the seat gate reads the owner channel's settings from
// DIR/scratch.json in place of owner/scratch.json of the state folder, so that
// one state folder, a phone trial's, keeps its own settings, its gate identity,
// its pins and its paired phones while a scratch daemon serves it. The name
// follows the flags of run: --owner-channel says what is served, --scratch and
// --seat-gate name what it is served from, and --owner-settings names the
// folder of the owner channel's settings. The value is a folder, not a file,
// because the folder is what is checked and held: the daemon opens it once by
// its handle and reads scratch.json beneath that handle every time.
//
// Where the folder lies: beside the state and runtime roots, not inside them
// (the runbook's layout puts it at ROOT/owner-settings, beside ROOT/runtime,
// with the state root elsewhere). The proof's daemon still reads the trial's
// gate identity, pins and key list and keeps its cards in the trial's journal;
// what the folder keeps apart is the settings: the trial's owner/scratch.json
// stays where it is and is not read, and the daemon reads the proof's own.
//
// Before anything else is read the folder is resolved through every link and
// `..`, opened with no link followed (openat2, RESOLVE_NO_SYMLINKS), and
// refused, by name, when it is not a private folder of the daemon's user, or
// when it is, lies in or holds:
//   - owner/ of the state folder, the trial's own settings folder;
//   - the state folder or the runtime folder (the folder sits beside them);
//   - a path a lead's sandbox reaches (leaddaemon.ScratchLeadReach: the host
//     paths every bind table binds, the agent install, the probe and each
//     lead's folder, mirror, mailbox, published folder and holder runtime
//     folder, whether or not the lead is defined);
//   - the gate's identity (owner/gate);
//   - once the settings are read, the drop folder, the mirror, the work
//     folder or the sandbox template they name (namedGuards).
// Each is decided two ways: on the resolved paths, and on the identity
// (device and inode) of the open folder and of each folder above it, which
// also catches a bind mount of a guarded folder itself given as the folder.
//
// What the checks are for: they guard against mistakes (a wrong path, a link,
// a folder of the trial's given by its other name), not against an attacker.
// A mount layout the owner or root makes is outside them: a bind mount of a
// folder within a guard, nested mounts, a mount made after the check and a
// mount in another namespace are not looked at. A lead cannot make a mount
// on the host. The file scratch.json is opened beneath the folder's handle
// without following a link and refused when it is not a regular private file
// of the user with one name or is the key list, the journal, a file of the
// gate or the trial's settings file. Its bytes are read once, from the
// descriptor that was checked, and decoded from those bytes; nothing opens it
// by its name again. A link planted after the check changes nothing: the
// folder is the one open, and the file is opened beneath its handle with
// O_NOFOLLOW and compared with what was checked after it is read. What stays outside the check: the folder is the
// user's own, so a process of the user's (not a lead's: the folder is out of
// every lead's reach) could still change the file between two reads; the
// daemon reads it twice, before its locks and when it builds the service, and
// each read is checked.

// settingsName is the settings file in the folder of --owner-settings.
const settingsName = hostadapter.ScratchName

// maxSettingsBytes is the largest settings file, the host adapter's bound.
const maxSettingsBytes = hostadapter.MaxScratchBytes

// maxUnixSocketPath is the longest path a Unix socket address holds, the
// bound of package clientapi.
const maxUnixSocketPath = 107

// ownerSettingsHook, when set, is called with "opened" once the folder is
// open and with "file" once the settings file is open, before it is read. Only
// a test sets it, to plant a link between the check and the read.
var ownerSettingsHook func(stage string)

// defineSettingsFlags registers --owner-settings and --check.
func defineSettingsFlags(fs *flag.FlagSet, s *settingsFlags) {
	fs.Func("owner-settings", "read the owner channel's settings from DIR/scratch.json, not owner/scratch.json of the state folder (with --scratch and --owner-channel)", func(v string) error {
		s.dir, s.dirSet = v, true
		return nil
	})
	fs.BoolVar(&s.check, "check", false, "with --owner-settings: check and print what the daemon would read, then exit; nothing is bound, locked or started")
	fs.BoolVar(&s.keysPrefix, "keys-prefix", false, "with --check: check the state folder's key list and its head with the journal's own checks, read only, and print the list's prefix, LEN:SHA256, and the SHA-256 of its head, from the one snapshot checked")
	fs.Func("keys-since", "with --check: check the key list and its head, and name each record appended to the list since the prefix LEN:SHA256", func(v string) error {
		s.keysSince, s.sinceSet = v, true
		return nil
	})
	fs.Func("expect-state", "with --check: refuse, before any file of the state folder is read, unless DIR is the state folder of the scratch config (the same device and inode)", func(v string) error {
		s.expectState, s.expectSet = v, true
		return nil
	})
}

// sameStateFolder returns nil when expect, the folder of --expect-state, is
// the folder state, the state root the scratch config of this invocation
// decoded to: the same device and inode, both stat'ed now. It reads nothing
// in either folder.
func sameStateFolder(expect, state string) error {
	var a, b unix.Stat_t
	if err := unix.Stat(expect, &a); err != nil {
		return fmt.Errorf("the folder %s cannot be looked at: %v", expect, err)
	}
	if err := unix.Stat(state, &b); err != nil {
		return fmt.Errorf("the state folder %s of the scratch config cannot be looked at: %v", state, err)
	}
	if a.Mode&unix.S_IFMT != unix.S_IFDIR || idOfStat(&a) != idOfStat(&b) {
		return fmt.Errorf("%s is not the state folder of the scratch config, %s (another device and inode): this check would be of another trial; give the config of this trial", expect, state)
	}
	return nil
}

// The refusals of the folder, by name. Each is one line of --check. The
// last, ruleNamed, is decided once the settings are read, on the folders they
// name.
const (
	ruleTrial  = "not the trial's settings folder"
	ruleRoots  = "beside the state and runtime folders, not inside them"
	ruleLead   = "out of every lead's reach"
	ruleSecret = "not the key list, the journal or the gate identity"
	ruleNamed  = "apart from the folders the settings name"
)

// settingsGuard is one path the folder must not be, lie in or hold.
type settingsGuard struct {
	rule, path, what string
}

// fileID is the identity of a file: its device and inode.
type fileID struct{ dev, ino uint64 }

func idOfStat(st *unix.Stat_t) fileID { return fileID{uint64(st.Dev), st.Ino} }

// settingsPlace is what the check of the folder needs: the folder given, the
// roots and the scratch config.
type settingsPlace struct {
	dir, state, runtime string
	cfg                 *leaddaemon.Scratch
	getenv              func(string) string
}

// guards are every path the folder is held away from, by rule.
func (p settingsPlace) guards() ([]settingsGuard, error) {
	owner := filepath.Join(p.state, hostadapter.OwnerDirName)
	out := []settingsGuard{
		{ruleTrial, owner, "owner/ of the state folder, the trial's settings folder"},
		{ruleRoots, p.state, "the state folder"},
		{ruleRoots, p.runtime, "the runtime folder"},
		{ruleSecret, filepath.Join(owner, leaddaemon.SeatGateDirName), "the gate's identity and pins (owner/gate)"},
		{ruleSecret, filepath.Join(owner, ownerjournal.KeysFileName), "the key list (owner/keys.log)"},
		{ruleSecret, filepath.Join(owner, hostadapter.JournalName), "the journal (owner/journal.db)"},
	}
	reach, err := leaddaemon.ScratchLeadReach(p.cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("the reach of the leads cannot be worked out: %v", err)
	}
	for _, r := range reach {
		out = append(out, settingsGuard{ruleLead, r.Path, r.What})
	}
	return out, nil
}

// secretFiles are the files the settings file must not be, by what they are.
func (p settingsPlace) secretFiles() []settingsGuard {
	owner := filepath.Join(p.state, hostadapter.OwnerDirName)
	gate := filepath.Join(owner, leaddaemon.SeatGateDirName)
	return []settingsGuard{
		{ruleSecret, filepath.Join(owner, ownerjournal.KeysFileName), "the key list (owner/keys.log)"},
		{ruleSecret, filepath.Join(owner, ownerjournal.KeysHeadFileName), "the key list's head (owner/keys.head)"},
		{ruleSecret, filepath.Join(owner, hostadapter.JournalName), "the journal (owner/journal.db)"},
		{ruleSecret, filepath.Join(gate, "key.pem"), "the gate's key (owner/gate/key.pem)"},
		{ruleSecret, filepath.Join(gate, "cert.pem"), "the gate's certificate (owner/gate/cert.pem)"},
		{ruleSecret, filepath.Join(gate, leaddaemon.SeatPinsFileName), "the seat pins (owner/gate/seat-pins)"},
		{ruleTrial, filepath.Join(owner, hostadapter.ScratchName), "the trial's settings file (owner/scratch.json)"},
	}
}

// namedGuards are the paths the settings name that the folder holding them
// must not be, lie in or hold: the drop folder, the one place another process
// is expected to write, the mirror and the work folder, which others write or
// run in, and the sandbox template, which decides which command wraps the
// gates (checkScratchRoots keeps the template out of the first three for the
// same reason). The settings file names the template and the gates, so it is
// of the same weight.
func namedGuards(sc hostadapter.Scratch) []settingsGuard {
	out := []settingsGuard{
		{ruleNamed, sc.Drop, "the drop folder the settings name"},
		{ruleNamed, sc.Mirror, "the mirror the settings name"},
	}
	if sc.Work != "" {
		out = append(out, settingsGuard{ruleNamed, sc.Work, "the work folder the settings name"})
	}
	if sc.Sandbox != "" {
		out = append(out, settingsGuard{ruleNamed, sc.Sandbox, "the sandbox template the settings name"})
	}
	return out
}

// apartFromNamed refuses a folder that is, lies in or holds a path its own
// settings name ([namedGuards]), by path and identity, as every rule.
func (d *settingsDir) apartFromNamed(sc hostadapter.Scratch) error {
	for _, g := range namedGuards(sc) {
		if err := d.against(g); err != nil {
			return err
		}
	}
	return nil
}

// settingsDir is the folder of --owner-settings, open by its handle.
type settingsDir struct {
	given, real string
	f           *os.File
	id          fileID
	// up are the identities of the folder and of every folder above it, as
	// the open folder's ".." leads.
	up []fileID
}

// openSettingsDir resolves the folder, opens it with no link followed and
// checks that it is a private folder of the user's. Every refusal names the
// folder as given.
func openSettingsDir(dir string) (*settingsDir, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\x00\n") {
		return nil, fmt.Errorf("%q is not a clean absolute path", dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("the folder %s cannot be resolved: %v", dir, err)
	}
	how := unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	var fd int
	for {
		fd, err = unix.Openat2(unix.AT_FDCWD, real, &how)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("the folder %s changed while it was resolved (a link on the way to %s)", dir, real)
		}
		return nil, fmt.Errorf("the folder %s (%s) cannot be opened without following a link: %v", dir, real, err)
	}
	d := &settingsDir{given: dir, real: real, f: os.NewFile(uintptr(fd), real)}
	if ownerSettingsHook != nil {
		ownerSettingsHook("opened")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		d.f.Close()
		return nil, err
	}
	d.id = idOfStat(&st)
	// The identities of the folder and the folders above it, through the
	// open handle: ".." of a folder, until the root, whose ".." is itself.
	d.up = append(d.up, d.id)
	cur := fd
	for i := 0; i < 4096; i++ {
		pfd, err := unix.Openat(cur, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if cur != fd {
			unix.Close(cur)
		}
		if err != nil {
			d.f.Close()
			return nil, fmt.Errorf("the folders above %s cannot be read: %v", dir, err)
		}
		var ps unix.Stat_t
		if err := unix.Fstat(pfd, &ps); err != nil {
			unix.Close(pfd)
			d.f.Close()
			return nil, err
		}
		pid := idOfStat(&ps)
		if pid == d.up[len(d.up)-1] {
			unix.Close(pfd)
			break
		}
		d.up = append(d.up, pid)
		cur = pfd
	}
	return d, nil
}

// private refuses a folder that is not a folder of the user's, closed to the
// group and to others.
func (d *settingsDir) private() error {
	var st unix.Stat_t
	if err := unix.Fstat(int(d.f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a folder", d.given)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not the daemon user's folder", d.given)
	}
	if st.Mode&0o077 != 0 {
		return fmt.Errorf("%s can be reached by the group or others (mode %04o): make it 0700", d.given, st.Mode&0o777)
	}
	return nil
}

// statID is the identity of path, following links, or false when it is not
// there.
func statID(path string) (fileID, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return fileID{}, false
	}
	return idOfStat(&st), true
}

// aboveIDs are the identities of the folders above the resolved path rp, up to
// the root, of those that are there.
func aboveIDs(rp string) []fileID {
	var out []fileID
	for p := filepath.Dir(rp); ; p = filepath.Dir(p) {
		if id, ok := statID(p); ok {
			out = append(out, id)
		}
		if p == "/" {
			return out
		}
	}
}

func hasID(list []fileID, id fileID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// against decides one guard: the folder must not be, lie in or hold it, by
// its resolved path and by the identities of the folders. A bind mount of the
// guard itself given as the folder has the guard's identity; a bind mount of a
// folder within the guard, or any other mount layout, is not looked at.
func (d *settingsDir) against(g settingsGuard) error {
	rp, err := resolvePath(g.path)
	if err != nil {
		return fmt.Errorf("%s (%s) cannot be resolved: %v", g.what, g.path, err)
	}
	in := fmt.Errorf("the folder %s is or lies in %s (%s)", d.given, g.what, g.path)
	holds := fmt.Errorf("the folder %s holds %s (%s)", d.given, g.what, g.path)
	if under(d.real, rp) {
		return in
	}
	if under(rp, d.real) {
		return holds
	}
	if id, ok := statID(rp); ok && hasID(d.up, id) {
		return fmt.Errorf("the folder %s is or lies in %s (%s), by another name (a bind mount)", d.given, g.what, g.path)
	}
	if hasID(aboveIDs(rp), d.id) {
		return fmt.Errorf("the folder %s holds %s (%s), by another name (a bind mount)", d.given, g.what, g.path)
	}
	return nil
}

// rules decides each rule of the folder: nil, or the first guard it fails.
func (d *settingsDir) rules(guards []settingsGuard) map[string]error {
	out := map[string]error{ruleTrial: nil, ruleRoots: nil, ruleLead: nil, ruleSecret: nil}
	for _, g := range guards {
		if out[g.rule] != nil {
			continue
		}
		out[g.rule] = d.against(g)
	}
	return out
}

// settingsFile is what the check of the settings file saw.
type settingsFile struct {
	st unix.Stat_t
}

// readSettings opens scratch.json beneath the folder's handle without
// following a link, checks it, reads its bytes once from the descriptor that
// was checked and decodes those bytes with the host adapter's strict reader
// (hostadapter.ReadScratch), then checks that the file is still the one that
// was checked. Nothing opens the file by its name a second time, so the
// bytes decoded are the bytes of the file checked. It refuses trial settings.
func (d *settingsDir) readSettings(secrets []settingsGuard) (hostadapter.Scratch, settingsFile, error) {
	var none hostadapter.Scratch
	name := d.given + "/" + settingsName
	fd, err := unix.Openat(int(d.f.Fd()), settingsName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return none, settingsFile{}, fmt.Errorf("there is no %s", name)
	case errors.Is(err, unix.ELOOP):
		return none, settingsFile{}, fmt.Errorf("%s is a link; the settings are a regular file", name)
	case err != nil:
		return none, settingsFile{}, fmt.Errorf("%s cannot be opened: %v", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var sf settingsFile
	if err := unix.Fstat(fd, &sf.st); err != nil {
		return none, sf, err
	}
	id := idOfStat(&sf.st)
	for _, s := range secrets {
		if sid, ok := statID(s.path); ok && sid == id {
			return none, sf, fmt.Errorf("%s is %s (%s), by another name", name, s.what, s.path)
		}
	}
	switch {
	case sf.st.Mode&unix.S_IFMT != unix.S_IFREG:
		return none, sf, fmt.Errorf("%s is not a regular file", name)
	case int(sf.st.Uid) != os.Geteuid():
		return none, sf, fmt.Errorf("%s is not the daemon user's file", name)
	case sf.st.Mode&0o077 != 0:
		return none, sf, fmt.Errorf("%s can be read by the group or others (mode %04o): make it 0600", name, sf.st.Mode&0o777)
	case sf.st.Nlink != 1:
		return none, sf, fmt.Errorf("%s has %d names (a hard link): the settings have one", name, sf.st.Nlink)
	case sf.st.Size > maxSettingsBytes:
		return none, sf, fmt.Errorf("%s is larger than %d bytes", name, maxSettingsBytes)
	}
	if ownerSettingsHook != nil {
		ownerSettingsHook("file")
	}
	// The host adapter's strict reader, on the bytes of the descriptor that
	// was checked: the file is read once, from its start, and never opened
	// again by a name.
	sc, err := hostadapter.ReadScratch(io.NewSectionReader(f, 0, maxSettingsBytes+1))
	if err != nil {
		return none, sf, fmt.Errorf("%s: %v", name, err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return none, sf, err
	}
	// The name still names the file that was read (/proc/self/fd/N is the
	// open folder itself, whatever its path names now), and the file did not
	// change while it was read.
	via := "/proc/self/fd/" + strconv.Itoa(int(d.f.Fd())) + "/" + settingsName
	var now unix.Stat_t
	if err := unix.Lstat(via, &now); err != nil || idOfStat(&now) != id || after.Mtim != sf.st.Mtim ||
		after.Ctim != sf.st.Ctim || after.Size != sf.st.Size {
		return none, sf, fmt.Errorf("%s changed while it was read", name)
	}
	if sc.Trial {
		return none, sf, fmt.Errorf(`the settings %s are a phone trial's ("trial": true): give the proof's own settings`, name)
	}
	return sc, sf, nil
}

// openOwnerSettings is the daemon's side of --owner-settings: the folder is
// opened and every rule decided, and the reader it returns reads the settings
// beneath the open folder, checked each time.
func openOwnerSettings(p settingsPlace) (scratchLoad, error) {
	d, err := openSettingsDir(p.dir)
	if err != nil {
		return nil, err
	}
	if err := d.private(); err != nil {
		d.f.Close()
		return nil, err
	}
	guards, err := p.guards()
	if err != nil {
		d.f.Close()
		return nil, err
	}
	// Every rule the folder fails is named, in one line.
	rules := d.rules(guards)
	var failed []string
	for _, r := range []string{ruleTrial, ruleRoots, ruleLead, ruleSecret} {
		if rules[r] != nil {
			failed = append(failed, r+": "+rules[r].Error())
		}
	}
	if len(failed) > 0 {
		d.f.Close()
		return nil, errors.New(strings.Join(failed, "; "))
	}
	secrets := p.secretFiles()
	return func() (hostadapter.Scratch, error) {
		sc, _, err := d.readSettings(secrets)
		if err != nil {
			return sc, err
		}
		if err := d.apartFromNamed(sc); err != nil {
			return hostadapter.Scratch{}, fmt.Errorf("%s: %v", ruleNamed, err)
		}
		return sc, nil
	}, nil
}

// ownerSettingsRun takes --owner-settings and --check for ownerRun.
func ownerSettingsRun(a *app, o *ownerFlags, cfg *leaddaemon.Config) (scratchLoad, int) {
	s := o.settings
	switch {
	case cfg.Scratch == nil:
		fmt.Fprint(a.stderr, "ataniqd run: --owner-settings and --check need --scratch CONFIG\n")
		return nil, leaddaemon.ExitUsage
	case (s.keysPrefix || s.sinceSet) && !s.check:
		fmt.Fprint(a.stderr, "ataniqd run: --keys-prefix and --keys-since need --check\n")
		return nil, leaddaemon.ExitUsage
	case s.keysPrefix && s.sinceSet:
		fmt.Fprint(a.stderr, "ataniqd run: --keys-prefix and --keys-since exclude each other\n")
		return nil, leaddaemon.ExitUsage
	case s.expectSet && !s.check:
		fmt.Fprint(a.stderr, "ataniqd run: --expect-state needs --check\n")
		return nil, leaddaemon.ExitUsage
	}
	// The state folder this invocation decoded from its config is the one
	// the caller means, before anything in it is read.
	if s.expectSet {
		if err := sameStateFolder(s.expectState, cfg.StateDir); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd run: --expect-state: %s\n", leaddaemon.SafeText(err.Error()))
			return nil, leaddaemon.ExitConfig
		}
	}
	switch {
	case s.keysPrefix || s.sinceSet:
		// The key list alone: neither the settings folder nor the gate is
		// looked at.
		if err := a.requireInstance(*cfg); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return nil, leaddaemon.ExitCode(err)
		}
		return nil, a.keysCheck(s, cfg.StateDir)
	case !s.dirSet:
		fmt.Fprint(a.stderr, "ataniqd run: --check needs --owner-settings DIR\n")
		return nil, leaddaemon.ExitUsage
	}
	p := settingsPlace{dir: s.dir, state: cfg.StateDir, runtime: cfg.RuntimeDir, cfg: cfg.Scratch, getenv: a.getenv}
	if s.check {
		return nil, a.ownerSettingsCheck(o, cfg, p)
	}
	load, err := openOwnerSettings(p)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd run: --owner-settings: %s\n", leaddaemon.SafeText(err.Error()))
		return nil, leaddaemon.ExitConfig
	}
	return load, -1
}

// ownerSettingsCheck is run --check: every check the daemon makes before it
// takes a lock, on the same flags and config, and one the start does not
// make, the socket paths of every lead's holder (a holder meets that limit
// only when it starts, during the sitting), one line each, with what passes
// and why a refusal fails, then what the daemon would read. It binds, locks,
// makes and starts nothing. An identity refusal has the same exit code as run.
func (a *app) ownerSettingsCheck(o *ownerFlags, cfg *leaddaemon.Config, p settingsPlace) int {
	w := a.stdout
	fails := 0
	line := func(name string, err error, ok string) {
		if err != nil {
			fails++
			fmt.Fprintf(w, "FAIL  %s: %s\n", name, leaddaemon.SafeText(err.Error()))
			return
		}
		fmt.Fprintf(w, "pass  %s: %s\n", name, leaddaemon.SafeText(ok))
	}
	// A check that depends on one that failed is not made: its line says
	// so and adds no refusal of its own.
	skip := func(name, why string) {
		fmt.Fprintf(w, "skip  %s: not checked: %s\n", name, why)
	}
	fmt.Fprintf(w, "check: the scratch daemon on the state folder %s and the runtime folder %s, with the owner channel's settings from %s\n",
		leaddaemon.SafeText(p.state), leaddaemon.SafeText(p.runtime), leaddaemon.SafeText(p.dir))
	owner := filepath.Join(p.state, hostadapter.OwnerDirName)
	var sc hostadapter.Scratch
	scOK := false
	d, err := openSettingsDir(p.dir)
	if err != nil {
		line("the settings folder", err, "")
		for _, r := range []string{"a private folder of the daemon's user", ruleTrial, ruleRoots, ruleLead, ruleSecret, "the settings file", "the settings", ruleNamed, "the scratch roots, the drop folder and the mirror", "the suite"} {
			skip(r, "the settings folder is refused")
		}
	} else {
		defer d.f.Close()
		line("the settings folder", nil, p.dir+", resolved to "+d.real+", opened by its handle with no link followed")
		perr := d.private()
		line("a private folder of the daemon's user", perr, "mode 0700 or narrower")
		guards, gerr := p.guards()
		var rules map[string]error
		if gerr == nil {
			rules = d.rules(guards)
		}
		oks := map[string]string{
			ruleTrial:  "is not, lies not in and holds not " + owner,
			ruleRoots:  "lies beside " + p.state + " and " + p.runtime + ": is not, lies not in and holds not either",
			ruleLead:   fmt.Sprintf("is not, lies not in and holds not any of the %d paths the leads' sandboxes reach (their folders, mirrors, mailboxes, published folders, holder runtime folders, the agent install, the probe and the host paths every sandbox binds)", countRule(guards, ruleLead)),
			ruleSecret: "is not, lies not in and holds not " + filepath.Join(owner, leaddaemon.SeatGateDirName) + ", the key list or the journal",
		}
		for _, r := range []string{ruleTrial, ruleRoots, ruleLead, ruleSecret} {
			if gerr != nil {
				line(r, gerr, "")
				continue
			}
			line(r, rules[r], oks[r]+", by path and by identity")
		}
		folderOK := perr == nil && gerr == nil && rules[ruleTrial] == nil && rules[ruleRoots] == nil && rules[ruleLead] == nil && rules[ruleSecret] == nil
		if !folderOK {
			for _, r := range []string{"the settings file", "the settings", ruleNamed, "the scratch roots, the drop folder and the mirror", "the suite"} {
				skip(r, "the settings folder is refused, so nothing in it is read")
			}
		} else {
			var sf settingsFile
			sc, sf, err = d.readSettings(p.secretFiles())
			if err != nil {
				line("the settings file", err, "")
				for _, r := range []string{"the settings", ruleNamed, "the scratch roots, the drop folder and the mirror", "the suite"} {
					skip(r, "the settings file is refused")
				}
			} else {
				line("the settings file", nil, fmt.Sprintf("%s/%s: a regular file of the daemon's user, mode %04o, one name, %d bytes, not the key list, the journal, a file of the gate or the trial's settings",
					p.dir, settingsName, sf.st.Mode&0o777, sf.st.Size))
				line("the settings", nil, fmt.Sprintf("not a phone trial's; the stand-in door; project %s, role %s, target %s; scratch roots %s; drop folder %s",
					sc.Project, sc.Role, sc.Target, strings.Join(sc.Roots, ", "), sc.Drop))
				line(ruleNamed, d.apartFromNamed(sc), "is not, lies not in and holds not the drop folder, the mirror, the work folder or the sandbox template the settings name, by path and by identity")
				line("the scratch roots, the drop folder and the mirror", checkScratchRoots(p.state, sc, p.getenv),
					"none is, lies in or holds a default folder of the account; the mirror lies under a scratch root; the drop folder is apart from the state folder")
				if ownerSuite != nil {
					line("the suite", nil, "a test's suite")
				} else if _, err := sc.VerifySuiteOf(); err != nil {
					line("the suite", err, "")
				} else {
					line("the suite", nil, "the sandbox template "+sc.Sandbox+" and the work folder "+sc.Work+" are named (nothing is verified at start)")
				}
				scOK = true
			}
		}
	}
	leads, placeErr := p.cfg.Config.Leads()
	if placeErr == nil {
		placeErr = canaryroot.Check(p.cfg.Config.StateRoot, p.cfg.Config.RuntimeRoot, leads[0].Role.Project(), p.getenv)
	}
	line("the root marker", placeErr, "the scratch place's marker is in "+p.state+", and neither root is, lies in or holds a default folder of the account")
	if o.gateSet {
		var msg bytes.Buffer
		quiet := *a
		quiet.stderr = &msg
		g, code := quiet.seatGatePrepare(o.seatGate, p.state)
		if code >= 0 {
			line("the seat gate", errors.New(strings.TrimSpace(strings.TrimPrefix(msg.String(), "ataniqd run: --seat-gate: "))), "")
		} else {
			origin := "seat origin " + g.Origin
			if g.Origin == "" {
				origin = "no seat origin (" + g.NoOrigin + ")"
			}
			line("the seat gate", nil, fmt.Sprintf("%s, key fingerprint %s, %d seat pin(s), %s", g.Addr, g.Identity.Fingerprint(), len(g.Pins), origin))
		}
	} else {
		fmt.Fprintf(w, "note  the seat gate: no --seat-gate, so the daemon serves no gate\n")
	}
	exit := leaddaemon.ExitConfig
	if placeErr != nil {
		skip("instance identity", "the scratch place is refused")
	} else {
		identityErr := a.requireInstance(*cfg)
		if identityErr != nil && fails == 0 {
			exit = leaddaemon.ExitCode(identityErr)
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(identityErr.Error()))
		}
		line("instance identity", identityErr, p.state)
	}
	sockErr := socketPaths(p, leads)
	line("the socket paths", sockErr, fmt.Sprintf("the daemon's socket and every lead's holder sockets fit in a Unix socket address (%d bytes)", maxUnixSocketPath))
	var holder holderSocketError
	holderOnly := fails == 1 && errors.As(sockErr, &holder)
	fmt.Fprintf(w, "read  %s/%s: the owner channel's settings, read only\n", leaddaemon.SafeText(p.dir), settingsName)
	fmt.Fprintf(w, "read  %s: the gate's key, certificate and seat pins, read only, never written\n", leaddaemon.SafeText(filepath.Join(owner, leaddaemon.SeatGateDirName)))
	fmt.Fprintf(w, "read  %s and %s: the paired phones' keys, read; a pairing, a revocation or a raised sign counter appends to them\n",
		leaddaemon.SafeText(filepath.Join(owner, ownerjournal.KeysFileName)), ownerjournal.KeysHeadFileName)
	fmt.Fprintf(w, "open  %s and %s: the journal and the records the owner channel keeps its cards in\n",
		leaddaemon.SafeText(filepath.Join(owner, hostadapter.JournalName)), hostadapter.RecordsName+"/")
	fmt.Fprintf(w, "skip  %s: the state folder's own settings are not read\n", leaddaemon.SafeText(filepath.Join(owner, hostadapter.ScratchName)))
	if scOK {
		fmt.Fprintf(w, "open  %s: the drop folder of the owner channel's requests\n", leaddaemon.SafeText(sc.Drop))
	}
	fmt.Fprintf(w, "note  at start the daemon takes the state folder's lock (exit 12 while another daemon, the trial's, holds it), its runtime folder's (17) and the gate's port (18); --check takes none of them\n")
	switch {
	case holderOnly:
		// The daemon's start does not look at the holders' sockets: it
		// would start, and the lead's holder would fail at its own start.
		fmt.Fprintf(w, "check: %d refusal(s): the sitting failed preflight (exit %d): the daemon would start, but the start of the lead's holder would fail (its socket path does not fit); nothing was bound, locked, made or started\n", fails, leaddaemon.ExitConfig)
		return leaddaemon.ExitConfig
	case fails > 0:
		fmt.Fprintf(w, "check: %d refusal(s): the sitting failed preflight (exit %d): the daemon would not start; nothing was bound, locked, made or started\n", fails, exit)
		return exit
	}
	fmt.Fprintf(w, "check: no refusal: the daemon would read these and start; nothing was bound, locked, made or started\n")
	return leaddaemon.ExitOK
}

// countRule counts the guards of a rule.
func countRule(guards []settingsGuard, rule string) int {
	n := 0
	for _, g := range guards {
		if g.rule == rule {
			n++
		}
	}
	return n
}

// holderSocketError is the refusal of a holder's socket path that does not
// fit: the daemon's start does not check it, the holder's start fails on it.
type holderSocketError struct{ path string }

func (e holderSocketError) Error() string {
	return fmt.Sprintf("the holder socket %s is %d bytes, over the %d a Unix socket address holds: choose a shorter runtime root", e.path, len(e.path), maxUnixSocketPath)
}

// socketPaths refuses a daemon socket or a holder socket whose path does not
// fit in a Unix socket address.
func socketPaths(p settingsPlace, leads []leadcanary.Config) error {
	if err := clientapi.CheckSocketPath(p.runtime); err != nil {
		return err
	}
	for _, l := range leads {
		dir, err := l.HolderRuntimeDir()
		if err != nil {
			return err
		}
		for _, s := range []string{"attach.sock", "control.sock"} {
			if path := filepath.Join(dir, s); len(path) > maxUnixSocketPath {
				return holderSocketError{path}
			}
		}
	}
	return nil
}
