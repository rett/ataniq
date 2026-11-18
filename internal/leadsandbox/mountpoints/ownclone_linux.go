//go:build linux

package mountpoints

import (
	"errors"
	"io/fs"
	"path"

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
// The state root is opened by name. Each of the six folders is opened once,
// from the state root's handle, and its mode and owner are read from the
// descriptor that lookup returned; the tree's .git is opened once from the
// tree's handle, and its entries are looked up beneath that descriptor. So what
// is checked is the folder that was opened, and no folder is opened by its name
// after it was looked at.
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
	state, prob := openState(l.StateRoot)
	if prob != nil {
		rep.add(prob)
		return rep, nil
	}
	defer state.Close()
	cfg, _ := l.ConfigDir(in.Runtime)

	for _, f := range []struct {
		name, dir string
	}{{"tree", l.Tree()}, {"home", l.Home()}, {"config folder", cfg}, {"records folder", l.QargiRecords()}, {"requests folder", l.Requests()}, {"mail folder", l.Mail()}} {
		folder := checkFolder(state, l.StateRoot, f.name, f.dir, uid, &rep)
		if folder == nil {
			continue
		}
		if f.name == "tree" {
			checkGit(folder, f.dir, &rep)
		}
		folder.Close()
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

// checkFolder opens the folder at host, below the state root, from the state
// root's handle, and checks on the descriptor it got that the folder has mode
// 0700 and is owned by uid. A link, on the way or as the folder itself, and
// anything that is not a folder are refused by the open. It returns the handle
// of a usable folder, which the caller closes, or nil.
func checkFolder(state *safefile.Root, stateRoot, name, host string, uid uint32, rep *Report) *safefile.Root {
	folder, prob := subRoot(state, stateRoot, host)
	if prob != nil {
		prob.Detail = clip("the " + name + ": " + prob.Detail)
		rep.add(prob)
		return nil
	}
	info, err := folder.Lstat(".")
	if err != nil {
		rep.add(fromErr(host, err))
		folder.Close()
		return nil
	}
	if bits := info.Mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); bits != 0o700 {
		rep.add(problem(ErrMode, host, "the %s has mode %04o, want 0700", name, uint32(info.Mode.Perm())|specialBits(info.Mode)))
	}
	if info.UID != uid {
		rep.add(problem(ErrOwner, host, "the %s is owned by uid %d, want %d", name, info.UID, uid))
	}
	return folder
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

// checkGit checks the .git folder of the tree, through the tree's handle. The
// folder is opened once, from that handle, and every entry is looked up beneath
// the descriptor the open returned.
func checkGit(tree *safefile.Root, treeDir string, rep *Report) {
	git := treeDir + "/.git"
	g, err := tree.SubRoot(".git")
	switch {
	case err == nil:
	case isNotExist(err):
		rep.add(problem(ErrNotOwnClone, git, "the tree has no .git folder"))
		return
	case errors.Is(err, safefile.ErrSymlink):
		rep.add(problem(ErrSymlink, git, "the .git entry is a symbolic link"))
		return
	case errors.Is(err, safefile.ErrNotDirectory):
		// The open has refused the entry. This lookup only tells a .git file
		// from another type, for the wording and the kind of the refusal.
		info, lerr := tree.Lstat(".git")
		switch {
		case lerr == nil && info.IsRegular():
			rep.add(problem(ErrNotOwnClone, git, "a .git file means a worktree or a submodule, not a full clone"))
		case lerr == nil:
			rep.add(problem(ErrWrongType, git, "the .git entry is %s, not a folder", describe(info)))
		default:
			rep.add(fromErr(git, err))
		}
		return
	default:
		rep.add(fromErr(git, err))
		return
	}
	defer g.Close()
	afterSubRoot(git)
	for _, e := range cloneEntries {
		host := git + "/" + e.name
		ei, err := g.Lstat(e.name)
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
		switch _, err := g.Lstat(r.name); {
		case err == nil:
			rep.add(problem(ErrNotOwnClone, git+"/"+r.name, "%s", r.why))
		case !isNotExist(err):
			rep.add(fromErr(git+"/"+r.name, err))
		}
	}
}
