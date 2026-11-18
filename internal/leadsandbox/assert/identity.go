package assert

import (
	"fmt"
	"path"
	"strings"
)

// MaxIdentities is the most file identities a spec lists, and the most
// CollectIdentities returns.
const MaxIdentities = 2048

// MaxCoordinatorIdentities is the most identities of the coordinators' folders
// a spec lists. It is lower than MaxIdentities so that both lists, full, fit a
// spec of MaxSpecBytes.
const MaxCoordinatorIdentities = 1024

// maxIdentityMounts is the most mounts of the mount table whose identity the
// probe reads. A table with more is not read to its end: the rest is counted
// and assertion 1 is not-checked.
const maxIdentityMounts = 2048

// CollectIdentities is the holder's helper for the spec's list of forbidden
// identities: it reads, outside the sandbox, the identity of each path of paths
// that exists and of each of its ancestors up to, and not including, the root
// "/". The list holds the owner's paths and the holder's, so that a bind of an
// owner folder, or of a folder above one, is recognised by what the kernel
// calls the file whatever path or mount root it has inside (a subvolume or a
// file system of its own, whose mount root is relative to itself, has none the
// text of a path could match).
//
// stat reads the identity of a path without following a link at its end, as
// [System.Identity] does ([HostSystem] has it); the paths should be resolved
// by the caller (a link in the middle of an ancestor chain is followed by the
// kernel, but a path that is itself a link has the identity of the link). A
// path or an ancestor that does not exist is skipped, not an error: an absent
// owner path is checked by assertion 6. Any other failure of stat is an error,
// and so are a path that is not clean and absolute, a path that is the root,
// and more than MaxIdentities results: a list that is cut short would make the
// check look better than it is. The result has no repeats, in the order the
// paths and their ancestors are met (a path, then its parent and so on).
func CollectIdentities(paths []string, stat func(path string) (FileIdentity, error)) ([]FileIdentity, error) {
	if stat == nil {
		return nil, refuse("identities", "no way to read the identity of a path")
	}
	var out []FileIdentity
	seen := map[FileIdentity]bool{}
	visited := map[string]bool{}
	for _, p := range paths {
		if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
			return nil, refuse("identities", "path %s is not a clean absolute path other than the root", clipQuote(p))
		}
		for q := p; q != "/"; q = path.Dir(q) {
			if visited[q] {
				break // this path and every ancestor of it were read
			}
			visited[q] = true
			id, err := stat(q)
			switch {
			case err == nil:
			case absent(err):
				continue
			default:
				return nil, fmt.Errorf("assert: identity of %s: %w", clipQuote(q), err)
			}
			if seen[id] {
				continue
			}
			if len(out) >= MaxIdentities {
				return nil, refuse("identities", "more than %d identities", MaxIdentities)
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// ancestors returns the path and its ancestors up to, not including, the root,
// nearest first.
func ancestors(p string) []string {
	var out []string
	for q := path.Clean(p); q != "/" && q != "." && q != ""; q = path.Dir(q) {
		out = append(out, q)
	}
	return out
}
