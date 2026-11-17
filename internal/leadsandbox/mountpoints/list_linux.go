//go:build linux

package mountpoints

import (
	"path"
	"strings"

	"ataniq/internal/leadsandbox/profile"
)

// RootKind names the lead-writable host folder a mount point lies in.
type RootKind string

// The folders a lead can write.
const (
	RootHome    RootKind = "home"
	RootConfig  RootKind = "config"
	RootTree    RootKind = "tree"
	RootRecords RootKind = "records"
)

// Point is one bind target that lies in a lead-writable host folder.
type Point struct {
	// Inside is the target path inside the sandbox.
	Inside string
	// HostPath is where the mount point lies on the host.
	HostPath string
	// Root is the folder it lies in, RootDir that folder's host path, and
	// Rel the mount point's path below it (clean, never empty).
	Root    RootKind
	RootDir string
	Rel     string
	// Dir says the mount point is a folder; else an empty file.
	Dir bool
	// Git says the mount point is one of the tree's .git rows (config, hooks,
	// info), which a clone has already.
	Git bool
	// Placeholder says the mount point is a tree placeholder outside .git:
	// it is listed in .git/info/exclude.
	Placeholder bool
}

// excludeRel is the path of the exclude file below the tree.
const excludeRel = ".git/info/exclude"

// List validates the inputs and returns the mount points that lie in a
// lead-writable host folder, in the order of the bind table (a parent before
// what lies below it). The error is a *Problem of kind ErrInvalid.
func List(in profile.Inputs) ([]Point, error) {
	t, err := in.Binds()
	if err != nil {
		p := problem(ErrInvalid, "", "%v", err)
		p.Err = err
		return nil, p
	}
	var out []Point
	for i, b := range t {
		if !b.LeadWritable {
			continue
		}
		if b.Kind != profile.KindRoBind && b.Kind != profile.KindRwBind {
			return nil, problem(ErrInvalid, b.Inside, "a %s row in a lead-writable folder cannot be made here", b.Kind)
		}
		host, ok := profile.HostMountPoint(t, i)
		if !ok {
			return nil, problem(ErrInvalid, b.Inside, "no host path for the mount point")
		}
		rootDir, kind, ok := rootOf(t, b.Within)
		if !ok {
			return nil, problem(ErrInvalid, b.Inside, "lies in %s, which is not a folder this package knows", b.Within)
		}
		rel := strings.TrimPrefix(host, rootDir+"/")
		if rel == host || rel == "" || path.Clean(rel) != rel {
			return nil, problem(ErrInvalid, host, "is not below %s", rootDir)
		}
		pt := Point{
			Inside: b.Inside, HostPath: host, Root: kind, RootDir: rootDir, Rel: rel, Dir: b.IsDir,
			Git:         b.SourceRole == profile.RoleTreeGit,
			Placeholder: b.SourceRole == profile.RoleExtra,
		}
		if pt.Placeholder {
			if err := checkExcludable(pt.Rel); err != nil {
				return nil, err
			}
		}
		out = append(out, pt)
	}
	return out, nil
}

// rootOf finds the read-write bind at inside and names its folder.
func rootOf(t []profile.Bind, inside string) (dir string, kind RootKind, ok bool) {
	for _, b := range t {
		if b.Kind != profile.KindRwBind || b.Inside != inside {
			continue
		}
		switch b.SourceRole {
		case profile.RoleHome:
			return b.Source, RootHome, true
		case profile.RoleConfig:
			return b.Source, RootConfig, true
		case profile.RoleTree:
			return b.Source, RootTree, true
		case profile.RoleRecords:
			return b.Source, RootRecords, true
		}
	}
	return "", "", false
}

// checkExcludable refuses a placeholder path that an anchored line of
// .git/info/exclude would not name exactly: a pattern character, or a
// trailing space, which git strips.
func checkExcludable(rel string) error {
	if strings.ContainsAny(rel, `\*?[]`) || strings.HasSuffix(rel, " ") {
		return problem(ErrInvalid, rel, "a tree placeholder path with a pattern character or a trailing space cannot be listed as one anchored line")
	}
	return nil
}

// excludeLine is the anchored line that lists a tree placeholder.
func excludeLine(rel string) string { return "/" + rel }

// listedIn reports whether the exclude file lists the line. The check is
// textual and conservative: the anchored line must be there, and no later line
// may start with "!". git lets a negation match by a pattern (a trailing
// space it strips, no leading slash, a star), and this check does not
// evaluate patterns, so any later negation that could match counts as
// un-listing the entry; it can report a benign negation as a missing line,
// never the reverse.
func listedIn(data []byte, rel string) bool {
	want := excludeLine(rel)
	listed := false
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSuffix(l, "\r")
		switch {
		case l == want:
			listed = true
		case strings.HasPrefix(l, "!"):
			listed = false
		}
	}
	return listed
}
