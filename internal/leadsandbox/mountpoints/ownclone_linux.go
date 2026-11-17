//go:build linux

package mountpoints

import (
	"io/fs"
	"path"
	"strings"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// cloneEntries are what a full clone's .git folder holds, with the type of
// each (true is a folder).
var cloneEntries = []struct {
	name string
	dir  bool
}{{"HEAD", false}, {"config", false}, {"objects", true}, {"refs", true}}

// CheckOwnClone checks that the tree is the lead's own full clone and that
// the folders of the lead are the owner's alone (the lead-hosting design,
// sections 3.3, 3.4 and 3.8 item 7):
//
//   - the tree, the lead's home, the runtime's config folder, the records
//     folder, the requests folder and the mail folder (the folders the lead
//     writes) are folders, not symbolic links, of mode 0700 and owned by uid
//     (the caller's user id), and no link lies on their path below the state
//     root;
//   - the tree's .git is a folder (a .git file means a worktree or submodule),
//     holds HEAD, config, objects and refs, and has no commondir and no
//     objects/info/alternates or objects/info/http-alternates (the objects of
//     another repository, which a sandbox may not have). .git/config is not
//     parsed: a core.worktree or core.bare setting is not looked for;
//   - no folder from the tree's parent up to and including the state root
//     holds a .git entry, so the tree does not sit below another repository.
//
// It is read-only and returns every problem it finds. The error is set only
// when the inputs are refused (a *Problem of kind ErrInvalid).
func CheckOwnClone(in profile.Inputs, uid uint32) (Report, error) {
	if err := in.Validate(); err != nil {
		p := problem(ErrInvalid, "", "%v", err)
		p.Err = err
		return Report{}, p
	}
	var rep Report
	l := in.Layout()
	state, err := safefile.OpenRoot(l.StateRoot)
	if err != nil {
		rep.add(fromErr(l.StateRoot, err))
		return rep, nil
	}
	defer state.Close()
	cfg, _ := l.ConfigDir(in.Runtime)

	treeOK := false
	for _, f := range []struct {
		name, dir string
	}{{"tree", l.Tree()}, {"home", l.Home()}, {"config folder", cfg}, {"records folder", l.QargiRecords()}, {"requests folder", l.Requests()}, {"mail folder", l.Mail()}} {
		ok := checkFolder(state, strings.TrimPrefix(f.dir, l.StateRoot+"/"), f.name, f.dir, uid, &rep)
		if f.name == "tree" {
			treeOK = ok
		}
	}
	if treeOK {
		checkGit(l.Tree(), &rep)
	}
	// The folders above the tree, up to the state root.
	for _, d := range []struct{ rel, host string }{
		{"leads/" + l.Role + "/.git", path.Join(l.LeadDir(), ".git")},
		{"leads/.git", path.Join(l.StateRoot, "leads", ".git")},
		{".git", path.Join(l.StateRoot, ".git")},
	} {
		_, err := state.Lstat(d.rel)
		switch {
		case err == nil:
			rep.add(problem(ErrNotOwnClone, d.host, "the tree lies below another repository"))
		case !isNotExist(err):
			rep.add(fromErr(d.host, err))
		}
	}
	return rep, nil
}

// checkFolder checks that rel, below the state root, is a folder of mode 0700
// owned by uid, and not a link. It reports whether it is a usable folder.
func checkFolder(state *safefile.Root, rel, name, host string, uid uint32, rep *Report) bool {
	info, err := state.Lstat(rel)
	if err != nil {
		rep.add(fromErr(host, err))
		return false
	}
	switch {
	case info.IsSymlink():
		rep.add(problem(ErrSymlink, host, "the %s is a symbolic link", name))
		return false
	case !info.IsDir():
		rep.add(problem(ErrWrongType, host, "the %s is %s, not a folder", name, describe(info)))
		return false
	}
	if bits := info.Mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); bits != 0o700 {
		rep.add(problem(ErrMode, host, "the %s has mode %04o, want 0700", name, uint32(info.Mode.Perm())|specialBits(info.Mode)))
	}
	if info.UID != uid {
		rep.add(problem(ErrOwner, host, "the %s is owned by uid %d, want %d", name, info.UID, uid))
	}
	return true
}

func specialBits(m fs.FileMode) uint32 {
	var b uint32
	if m&fs.ModeSetuid != 0 {
		b |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		b |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		b |= 0o1000
	}
	return b
}

// checkGit checks the .git folder of the tree.
func checkGit(treeDir string, rep *Report) {
	tree, err := safefile.OpenRoot(treeDir)
	if err != nil {
		rep.add(fromErr(treeDir, err))
		return
	}
	defer tree.Close()
	git := treeDir + "/.git"
	info, err := tree.Lstat(".git")
	switch {
	case err != nil && isNotExist(err):
		rep.add(problem(ErrNotOwnClone, git, "the tree has no .git folder"))
		return
	case err != nil:
		rep.add(fromErr(git, err))
		return
	case info.IsSymlink():
		rep.add(problem(ErrSymlink, git, "the .git entry is a symbolic link"))
		return
	case info.IsRegular():
		rep.add(problem(ErrNotOwnClone, git, "a .git file means a worktree or a submodule, not a full clone"))
		return
	case !info.IsDir():
		rep.add(problem(ErrWrongType, git, "the .git entry is %s, not a folder", describe(info)))
		return
	}
	for _, e := range cloneEntries {
		host := git + "/" + e.name
		ei, err := tree.Lstat(".git/" + e.name)
		switch {
		case err != nil && isNotExist(err):
			rep.add(problem(ErrNotOwnClone, host, "a full clone has it"))
		case err != nil:
			rep.add(fromErr(host, err))
		case ei.IsSymlink():
			rep.add(problem(ErrSymlink, host, "the entry is a symbolic link"))
		case e.dir && !ei.IsDir(), !e.dir && !ei.IsRegular():
			rep.add(problem(ErrWrongType, host, "the entry is %s", describe(ei)))
		}
	}
	for _, r := range []struct{ name, why string }{
		{"commondir", "a commondir means a linked worktree"},
		{"objects/info/alternates", "alternates borrow objects from another repository, so the tree is not a full clone"},
		{"objects/info/http-alternates", "alternates borrow objects from another repository, so the tree is not a full clone"},
	} {
		switch _, err := tree.Lstat(".git/" + r.name); {
		case err == nil:
			rep.add(problem(ErrNotOwnClone, git+"/"+r.name, "%s", r.why))
		case !isNotExist(err):
			rep.add(fromErr(git+"/"+r.name, err))
		}
	}
}
