//go:build linux

package mountpoints

import (
	"io/fs"
	"path"
	"sort"
	"strings"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// Modes of the git metadata folder and of what is in it, before the umask.
// The folder is Ataniq's alone: nothing the lead can reach is written there.
const (
	gitMetaDirMode  = 0o700
	gitMetaFileMode = 0o600
)

// Bounds on what is copied from the clone, which a lead could have written
// before the owner took it over: the size of .git/config and of each entry of
// .git/info, the number of entries of .git/info, and their total size.
const (
	maxGitConfig    = 1 << 20
	maxInfoFile     = 1 << 20
	maxInfoEntries  = 64
	maxInfoTotal    = 4 << 20
	infoRel         = ".git/info"
	configRel       = ".git/config"
	infoExcludeName = "exclude"
)

// metaCopy is what Prepare copies out of the clone into the git metadata
// folder: .git/config, and the regular files of .git/info, with the exclude
// file already carrying the placeholder lines.
type metaCopy struct {
	config []byte
	info   []infoFile
}

type infoFile struct {
	name string
	data []byte
}

// readCloneMeta reads what the folder is made from, through the tree's
// handle, and nothing else: .git/config (a regular file, not hard-linked, at
// most maxGitConfig bytes) and the regular files directly in .git/info (at
// most maxInfoEntries entries, maxInfoFile bytes each, maxInfoTotal in all,
// none hard-linked). A link, a folder or another type in .git/info is not
// copied. The clone's .git/hooks is never read: a managed lead's tree has no
// hooks. A .git/config that has an include or includeIf section (any case,
// found by a bounded scan of the section headers; no other key is
// interpreted) is refused with ErrInclude, and nothing is copied. Each
// placeholder of the tree outside .git that exclude does not list is added to
// the copy of exclude as one anchored line (the file is made when the clone
// has none).
func readCloneMeta(tree *safefile.Root, treeDir string, placeholders []Point) (*metaCopy, *Problem) {
	var m metaCopy
	var err error
	if m.config, err = tree.ReadFile(configRel, maxGitConfig, safefile.RefuseHardLinks()); err != nil {
		return nil, fromErr(treeDir+"/"+configRel, err)
	}
	// The copy is read by git inside the sandbox. An include or includeIf
	// section makes git read other files as configuration, and a relative
	// path of one lies next to the copy's place in the lead's tree. No other
	// key is interpreted here.
	if line, name := findInclude(m.config); name != "" {
		return nil, problem(ErrInclude, treeDir+"/"+configRel, "line %d has a [%s] section, which makes git read another file as configuration: remove it from the clone's .git/config and call Prepare again", line, name)
	}
	names, err := tree.ReadDirNames(infoRel, maxInfoEntries)
	if err != nil {
		return nil, fromErr(treeDir+"/"+infoRel, err)
	}
	total := 0
	for _, name := range names {
		host := treeDir + "/" + infoRel + "/" + name
		info, err := tree.Lstat(infoRel + "/" + name)
		if err != nil {
			return nil, fromErr(host, err)
		}
		if !info.IsRegular() {
			continue // a link, a folder or another type is not copied
		}
		data, err := tree.ReadFile(infoRel+"/"+name, maxInfoFile, safefile.RefuseHardLinks())
		if err != nil {
			return nil, fromErr(host, err)
		}
		if total += len(data); total > maxInfoTotal {
			return nil, problem(ErrUnreadable, treeDir+"/"+infoRel, "the regular files of the folder hold more than %d bytes in all", maxInfoTotal)
		}
		m.info = append(m.info, infoFile{name, data})
	}
	if len(placeholders) > 0 {
		m.addExclude(placeholders)
	}
	return &m, nil
}

// addExclude lists each placeholder that the copy of exclude does not list as
// one anchored line, and makes the file when there is none.
func (m *metaCopy) addExclude(placeholders []Point) {
	at := -1
	for i, f := range m.info {
		if f.name == infoExcludeName {
			at = i
		}
	}
	var data []byte
	if at >= 0 {
		data = m.info[at].data
	}
	var needed []string
	for _, p := range placeholders {
		if !listedIn(data, p.Rel) {
			needed = append(needed, excludeLine(p.Rel))
		}
	}
	if len(needed) == 0 {
		return
	}
	add := strings.Join(needed, "\n") + "\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		add = "\n" + add
	}
	data = append(append([]byte(nil), data...), add...)
	if at >= 0 {
		m.info[at].data = data
		return
	}
	m.info = append(m.info, infoFile{infoExcludeName, data})
	sort.Slice(m.info, func(i, j int) bool { return m.info[i].name < m.info[j].name })
}

// gitMetaTmpSuffix is added to the name of the git metadata folder to name the
// folder it is built in, a sibling in the lead's state folder.
const gitMetaTmpSuffix = ".tmp"

// gitMetaTmp is the temporary sibling the git metadata folder is built in.
func gitMetaTmp(gitMeta string) string { return path.Base(gitMeta) + gitMetaTmpSuffix }

// beforeGitMetaMove is called after the folder is built, synced and closed in
// its temporary place, and before it is moved into place. Nothing but a test
// sets it: it stands in for a Prepare that is interrupted at that moment.
var beforeGitMetaMove = func() error { return nil }

// writeGitMeta makes the folder: it builds it in a temporary sibling in the
// folder parent (the lead's state folder, which no sandbox binds and the lead
// cannot reach), mode 0700, its files 0600 and each created exclusively: config
// is a copy of the clone's, hooks is empty and info holds the copied files. The
// sibling itself is made with MkdirAll, which accepts a folder that exists:
// lookGitMeta refuses a leftover sibling before, and a second Prepare that
// races this one is stopped by the exclusive creation of the files. Each file and
// each folder is synced, then the whole is moved into its place with
// RENAME_NOREPLACE (nothing is ever replaced) and the folder that holds it is
// synced. So the place never holds a folder that is only partly written, even
// after an error part-way or a crash: it holds the whole folder or none. A
// failure leaves the temporary sibling, which lookGitMeta refuses (naming it)
// until it is removed by hand.
func writeGitMeta(parent *safefile.Root, in profile.Inputs, m *metaCopy) *Problem {
	tmp, final := gitMetaTmp(in.GitMeta), path.Base(in.GitMeta)
	tmpHost := path.Join(path.Dir(in.GitMeta), tmp)
	if err := parent.MkdirAll(tmp, gitMetaDirMode); err != nil {
		return fromErr(tmpHost, err)
	}
	if prob := fillGitMeta(tmpHost, m); prob != nil {
		return prob
	}
	if err := beforeGitMetaMove(); err != nil {
		return problem(ErrUnreadable, tmpHost, "the folder was built and not moved into place: %v", err)
	}
	if err := parent.RenameNoReplace(tmp, final); err != nil {
		return fromErr(in.GitMeta, err)
	}
	if err := parent.Sync("."); err != nil {
		return fromErr(path.Dir(in.GitMeta), err)
	}
	return nil
}

// fillGitMeta writes the entries of the folder at dir (made empty by the
// caller) and syncs every file and folder in it.
func fillGitMeta(dir string, m *metaCopy) *Problem {
	gm, err := safefile.OpenRoot(dir)
	if err != nil {
		return fromErr(dir, err)
	}
	defer gm.Close()
	write := func(rel string, data []byte) *Problem {
		if err := gm.WriteFileExclusive(rel, data, gitMetaFileMode); err != nil {
			return fromErr(dir+"/"+rel, err)
		}
		if err := gm.Sync(rel); err != nil {
			return fromErr(dir+"/"+rel, err)
		}
		return nil
	}
	if prob := write(profile.GitMetaConfig, m.config); prob != nil {
		return prob
	}
	for _, d := range []string{profile.GitMetaHooks, profile.GitMetaInfo} {
		if err := gm.MkdirAll(d, gitMetaDirMode); err != nil {
			return fromErr(dir+"/"+d, err)
		}
	}
	for _, f := range m.info {
		if prob := write(profile.GitMetaInfo+"/"+f.name, f.data); prob != nil {
			return prob
		}
	}
	for _, d := range []string{profile.GitMetaHooks, profile.GitMetaInfo, "."} {
		if err := gm.Sync(d); err != nil {
			return fromErr(dir+"/"+d, err)
		}
	}
	return nil
}

// checkGitMeta is the read-only check of the folder, for Check and for a
// Prepare that finds the folder made. The folder and its three entries must
// exist, be a folder (config a regular file), have no link on their path and
// have the modes Prepare gives them, and, when the table has tree
// placeholders, info/exclude must list each of them. It adds each problem to
// rep.
func checkGitMeta(rs *rootSet, in profile.Inputs, placeholders []Point, rep *Report) {
	gm, prob := rs.get(in.GitMeta)
	rep.add(prob)
	if gm == nil {
		return
	}
	if info, err := gm.Lstat("."); err != nil {
		rep.add(fromErr(in.GitMeta, err))
	} else {
		checkMode(info, in.GitMeta, "the git metadata folder", gitMetaDirMode, rep)
	}
	for _, e := range []struct {
		name string
		dir  bool
	}{{profile.GitMetaConfig, false}, {profile.GitMetaHooks, true}, {profile.GitMetaInfo, true}} {
		host := in.GitMeta + "/" + e.name
		info, err := gm.Lstat(e.name)
		switch {
		case err != nil:
			rep.add(fromErr(host, err))
			continue
		case info.IsSymlink():
			rep.add(problem(ErrSymlink, host, "the entry of the git metadata folder is a symbolic link"))
			continue
		case e.dir && !info.IsDir(), !e.dir && !info.IsRegular():
			rep.add(problem(ErrWrongType, host, "want %s, found %s", map[bool]string{true: "a folder", false: "a regular file"}[e.dir], describe(info)))
			continue
		}
		if e.dir {
			checkMode(info, host, "the entry", gitMetaDirMode, rep)
		} else {
			checkMode(info, host, "the entry", gitMetaFileMode, rep)
		}
	}
	if len(placeholders) == 0 {
		return
	}
	exPath := in.GitMeta + "/" + profile.GitMetaInfo + "/" + infoExcludeName
	data, err := gm.ReadFile(profile.GitMetaInfo+"/"+infoExcludeName, maxExclude, safefile.RefuseHardLinks())
	if err != nil && !isNotExist(err) {
		rep.add(fromErr(exPath, err))
		return
	}
	for _, p := range placeholders {
		switch {
		case err != nil:
			rep.add(problem(ErrNotExcluded, p.HostPath, "%s does not exist", exPath))
		case !listedIn(data, p.Rel):
			rep.add(problem(ErrNotExcluded, p.HostPath, "%s has no line %s", exPath, excludeLine(p.Rel)))
		}
	}
}

// checkMode reports a mode other than want.
func checkMode(info safefile.Info, host, what string, want fs.FileMode, rep *Report) {
	if bits := info.Mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); bits != want {
		rep.add(problem(ErrMode, host, "%s has mode %04o, want %04o", what, uint32(info.Mode.Perm())|specialBits(info.Mode), uint32(want)))
	}
}

// placeholdersOf are the tree placeholders of the points.
func placeholdersOf(pts []Point) []Point {
	var ph []Point
	for _, p := range pts {
		if p.Placeholder {
			ph = append(ph, p)
		}
	}
	return ph
}
