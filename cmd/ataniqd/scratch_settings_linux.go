//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
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
// Before anything else is read the folder is resolved through every link and
// `..`, opened with no link followed (openat2, RESOLVE_NO_SYMLINKS), and
// refused, by name, when it is not a private folder of the daemon's user, or
// when it is, lies in or holds:
//   - owner/ of the state folder, the trial's own settings folder;
//   - the state folder or the runtime folder;
//   - a path a lead's sandbox reaches (leaddaemon.ScratchLeadReach: the host
//     paths every bind table binds, the agent install, the probe and each
//     lead's folder, mirror, mailbox, published folder and holder runtime
//     folder, whether or not the lead is defined);
//   - the gate's identity (owner/gate).
// Each is decided three ways: on the resolved paths; on the identity (device
// and inode) of the open folder and of each folder above it, which catches a
// bind mount of a guarded folder itself; and on the places of the folder and of
// the guard within their filesystems, from the daemon's mount table
// (scratch_mounts_linux.go), which catches a bind mount of a folder within a
// guard given as the folder and a mount that shows the folder, or a part of
// it, within a guard (inside a lead's folder, say). A mount made after the
// check, or in another mount namespace, is not seen; only the owner or root
// can make one on the host. The file
// scratch.json is opened beneath the folder's handle without following a link
// and refused when it is not a regular private file of the user with one name
// or is the key list, the journal, a file of the gate or the trial's settings
// file. A link planted after the check changes nothing: the folder is the one
// open, and the file is read beneath its handle with O_NOFOLLOW and compared
// with what was checked after it is read. What stays outside the check: the
// folder is the user's own, so a process of the user's (not a lead's: the
// folder is out of every lead's reach) could still change the file between two
// reads; the daemon reads it twice, before its locks and when it builds the
// service, and each read is checked.

// settingsName is the settings file in the folder of --owner-settings.
const settingsName = hostadapter.ScratchName

// maxSettingsBytes is the largest settings file, the host adapter's bound.
const maxSettingsBytes = 64 << 10

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
}

// The refusals of the folder, by name. Each is one line of --check.
const (
	ruleTrial  = "not the trial's settings folder"
	ruleRoots  = "outside the state and runtime folders"
	ruleLead   = "out of every lead's reach"
	ruleSecret = "not the key list, the journal or the gate identity"
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

// settingsDir is the folder of --owner-settings, open by its handle.
type settingsDir struct {
	given, real string
	f           *os.File
	id          fileID
	// up are the identities of the folder and of every folder above it, as
	// the open folder's ".." leads.
	up []fileID
	// mnt is the daemon's mount table, read once when the folder is opened,
	// and place the folder's place within its filesystem.
	mnt   mountTable
	place fsPlace
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
	// The folder's place in its filesystem: the mount the handle lies on,
	// from the mount table, and the handle's own path below its mount point.
	if err := d.placeSelf(fd); err != nil {
		d.f.Close()
		return nil, fmt.Errorf("the folder %s cannot be placed in its filesystem: %v", dir, err)
	}
	return d, nil
}

// placeSelf reads the mount table and places the open folder in it, by the
// path the kernel gives its handle (/proc/self/fd/N, which follows the folder
// when it is renamed), on the mount the handle lies on.
func (d *settingsDir) placeSelf(fd int) error {
	t, err := readMountTable()
	if err != nil {
		return err
	}
	id, err := mountIDOfFD(fd)
	if err != nil {
		return err
	}
	at, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return err
	}
	if !filepath.IsAbs(at) || filepath.Clean(at) != at {
		return fmt.Errorf("the open folder has no path from the root (%q)", at)
	}
	pl, err := t.placeOf(id, at)
	if err != nil {
		return err
	}
	d.mnt, d.place = t, pl
	return nil
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
// its resolved path, by the identities of the folders and by their places in
// their filesystems.
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
	id, err := mountIDOfPath(rp)
	if err != nil {
		return fmt.Errorf("%s (%s) cannot be placed in its filesystem: %v", g.what, g.path, err)
	}
	pl, err := d.mnt.placeOf(id, rp)
	if err != nil {
		return fmt.Errorf("%s (%s) cannot be placed in its filesystem: %v", g.what, g.path, err)
	}
	switch m := d.mnt.meet(d.real, d.place, rp, pl); {
	case m.in:
		return fmt.Errorf("the folder %s is or lies in %s (%s), by another name: %s", d.given, g.what, g.path, m.how)
	case m.holds:
		return fmt.Errorf("the folder %s holds %s (%s), by another name: %s", d.given, g.what, g.path, m.how)
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
// following a link, checks it, reads it with the host adapter's strict reader
// and checks that what was read is the file that was checked. It refuses trial
// settings.
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
	defer unix.Close(fd)
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
	// The host adapter's strict reader, on the file beneath the folder's
	// handle: /proc/self/fd/N names the open folder itself, whatever its
	// path names now, and the reader opens the last name without following
	// a link.
	via := "/proc/self/fd/" + strconv.Itoa(int(d.f.Fd())) + "/" + settingsName
	sc, err := hostadapter.LoadScratch(via)
	if err != nil {
		return none, sf, errors.New(strings.ReplaceAll(err.Error(), via, name))
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return none, sf, err
	}
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
		return sc, err
	}, nil
}

// ownerSettingsRun takes --owner-settings and --check for ownerRun.
func ownerSettingsRun(a *app, o *ownerFlags, cfg *leaddaemon.Config) (scratchLoad, int) {
	s := o.settings
	switch {
	case cfg.Scratch == nil:
		fmt.Fprint(a.stderr, "ataniqd run: --owner-settings and --check need --scratch CONFIG\n")
		return nil, leaddaemon.ExitUsage
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
// takes a lock, on the same flags and config, one line each, with what passes
// and why a refusal fails, then what the daemon would read. It binds, locks,
// makes and starts nothing. It ends 0 when nothing is refused, else 10.
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
		for _, r := range []string{"a private folder of the daemon's user", ruleTrial, ruleRoots, ruleLead, ruleSecret, "the settings file", "the settings", "the scratch roots, the drop folder and the mirror", "the suite"} {
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
			ruleRoots:  "is not, lies not in and holds not " + p.state + " or " + p.runtime,
			ruleLead:   fmt.Sprintf("is not, lies not in and holds not any of the %d paths the leads' sandboxes reach (their folders, mirrors, mailboxes, published folders, holder runtime folders, the agent install, the probe and the host paths every sandbox binds)", countRule(guards, ruleLead)),
			ruleSecret: "is not, lies not in and holds not " + filepath.Join(owner, leaddaemon.SeatGateDirName) + ", the key list or the journal",
		}
		for _, r := range []string{ruleTrial, ruleRoots, ruleLead, ruleSecret} {
			if gerr != nil {
				line(r, gerr, "")
				continue
			}
			line(r, rules[r], oks[r]+", by path, by identity and by place in the filesystem")
		}
		folderOK := perr == nil && gerr == nil && rules[ruleTrial] == nil && rules[ruleRoots] == nil && rules[ruleLead] == nil && rules[ruleSecret] == nil
		if !folderOK {
			for _, r := range []string{"the settings file", "the settings", "the scratch roots, the drop folder and the mirror", "the suite"} {
				skip(r, "the settings folder is refused, so nothing in it is read")
			}
		} else {
			var sf settingsFile
			sc, sf, err = d.readSettings(p.secretFiles())
			if err != nil {
				line("the settings file", err, "")
				for _, r := range []string{"the settings", "the scratch roots, the drop folder and the mirror", "the suite"} {
					skip(r, "the settings file is refused")
				}
			} else {
				line("the settings file", nil, fmt.Sprintf("%s/%s: a regular file of the daemon's user, mode %04o, one name, %d bytes, not the key list, the journal, a file of the gate or the trial's settings",
					p.dir, settingsName, sf.st.Mode&0o777, sf.st.Size))
				line("the settings", nil, fmt.Sprintf("not a phone trial's; the stand-in door; project %s, role %s, target %s; scratch roots %s; drop folder %s",
					sc.Project, sc.Role, sc.Target, strings.Join(sc.Roots, ", "), sc.Drop))
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
	leads, err := p.cfg.Config.Leads()
	if err != nil {
		line("the root marker", err, "")
	} else {
		line("the root marker", canaryroot.Check(p.cfg.Config.StateRoot, p.cfg.Config.RuntimeRoot, leads[0].Role.Project(), p.getenv),
			"the scratch place's marker is in "+p.state+", and neither root is, lies in or holds a default folder of the account")
	}
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
	line("the socket paths", socketPaths(p, leads), fmt.Sprintf("the daemon's socket and every lead's holder sockets fit in a Unix socket address (%d bytes)", maxUnixSocketPath))
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
	if fails > 0 {
		fmt.Fprintf(w, "check: %d refusal(s): the daemon would not start (exit %d); nothing was bound, locked, made or started\n", fails, leaddaemon.ExitConfig)
		return leaddaemon.ExitConfig
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
			if n := len(filepath.Join(dir, s)); n > maxUnixSocketPath {
				return fmt.Errorf("the holder socket %s is %d bytes, over the %d a Unix socket address holds: choose a shorter runtime root", filepath.Join(dir, s), n, maxUnixSocketPath)
			}
		}
	}
	return nil
}
