package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/intake/execconfig"
)

// maxLinkTarget is the longest symlink target that is read.
const maxLinkTarget = 4096

// treeEntry is one entry of ls-tree -r.
type treeEntry struct {
	mode uint32
	typ  string
	id   string
	path string
}

// treeEntries lists the entries of a commit's tree, recursively, with
// ls-tree. More entries than the path limit is ErrLimit.
func (r *runner) treeEntries(ctx context.Context, tip string, lim Limits) ([]treeEntry, error) {
	sink := newCapture(lim.MaxPaths, maxFieldBytes, "tree entries")
	_, err := r.git(ctx, []string{"ls-tree", "-r", "-z", "--end-of-options", tip},
		nil, int64(lim.MaxPaths)*bytesPerRecord, sink)
	if err != nil {
		return nil, err
	}
	return parseTree(sink.buf)
}

func parseTree(out []byte) ([]treeEntry, error) {
	bad := errors.New("intake: unexpected output of git ls-tree")
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, bad
	}
	var entries []treeEntry
	for _, f := range bytes.Split(out[:len(out)-1], []byte{0}) {
		head, path, ok := strings.Cut(string(f), "\t")
		if !ok || path == "" {
			return nil, bad
		}
		parts := strings.Split(head, " ")
		if len(parts) != 3 || !gitenv.IsObjectID(parts[2]) {
			return nil, bad
		}
		mode, err := parseMode(parts[0])
		if err != nil {
			return nil, bad
		}
		if len(path) > maxPathBytes {
			return nil, errPathTooLong
		}
		entries = append(entries, treeEntry{mode: mode, typ: parts[1], id: parts[2], path: path})
	}
	return entries, nil
}

// link is a symlink of the tip's tree whose own path is execution config.
type link struct {
	path, pattern, reason string
	id                    string // the blob that holds the target text
	target                string // the repository path it stands for, once resolved
}

// linkTargets reads the target texts of the blobs with cat-file. A blob that
// is not a blob, is missing or is longer than maxLinkTarget has no entry in
// the result. It runs two commands, a size check and then the read, so that
// no long text is ever read.
func (r *runner) linkTargets(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list := []byte(strings.Join(ids, "\n") + "\n")
	out, err := r.git(ctx, []string{"cat-file", "--batch-check"}, bytes.NewReader(list),
		int64(len(ids))*256, nil)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, errors.New("intake: unexpected output of git cat-file")
	}
	var readable []string
	sizes := map[string]int{}
	for i, line := range lines {
		f := strings.Split(line, " ")
		if f[0] != ids[i] {
			return nil, errors.New("intake: unexpected output of git cat-file")
		}
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 {
			return nil, errors.New("intake: unexpected output of git cat-file")
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 0 {
			return nil, errors.New("intake: unexpected output of git cat-file")
		}
		if f[1] == "blob" && n <= maxLinkTarget {
			readable = append(readable, ids[i])
			sizes[ids[i]] = n
		}
	}
	targets := map[string]string{}
	if len(readable) == 0 {
		return targets, nil
	}
	list = []byte(strings.Join(readable, "\n") + "\n")
	out, err = r.git(ctx, []string{"cat-file", "--batch"}, bytes.NewReader(list),
		int64(len(readable))*(maxLinkTarget+256), nil)
	if err != nil {
		return nil, err
	}
	bad := errors.New("intake: unexpected output of git cat-file")
	for _, id := range readable {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 || string(out[:nl]) != fmt.Sprintf("%s blob %d", id, sizes[id]) {
			return nil, bad
		}
		out = out[nl+1:]
		if len(out) < sizes[id]+1 || out[sizes[id]] != '\n' {
			return nil, bad
		}
		targets[id] = string(out[:sizes[id]])
		out = out[sizes[id]+1:]
	}
	if len(out) != 0 {
		return nil, bad
	}
	return targets, nil
}

// resolveLink resolves the target text of a symlink at path link, lexically,
// against the symlink's folder inside the repository. It returns the
// repository path the link stands for, with no leading or trailing slash
// ("" is the repository root). When it cannot, it returns the problem as a
// short text. A system call that makes a symlink reads a C string, so the
// target ends at the first NUL.
func resolveLink(link, target string) (resolved, problem string) {
	if i := strings.IndexByte(target, 0); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return "", "its target is empty"
	}
	if strings.HasPrefix(target, "/") {
		return "", "its target is an absolute path"
	}
	var elems []string
	if i := strings.LastIndexByte(link, '/'); i >= 0 {
		elems = strings.Split(link[:i], "/")
	}
	for _, e := range strings.Split(target, "/") {
		switch e {
		case "", ".":
		case "..":
			if len(elems) == 0 {
				return "", "its target leaves the repository"
			}
			elems = elems[:len(elems)-1]
		default:
			elems = append(elems, e)
		}
	}
	return strings.Join(elems, "/"), ""
}

// lowerASCII folds A to Z only, as the matcher of execconfig does.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// aliasFlags adds the aliased flags to the changes.
//
// A symlink whose own path is execution config makes another folder stand
// for it: a symlink ".claude" that points at "cfg" makes "cfg/settings.json"
// agent settings. So every symlink of the tip's tree, changed or not, whose
// own path matches the rules (as a file or as a folder, because a link can
// stand for a folder) is resolved by its text, and every changed path that
// is its target or lies below it is flagged. A symlink whose target is empty,
// absolute, out of the repository or unreadable is flagged on its own path,
// when that path is changed.
func (r *runner) aliasFlags(ctx context.Context, rules execconfig.Set, tip string, changes []Change, lim Limits) error {
	if len(changes) == 0 {
		return nil
	}
	entries, err := r.treeEntries(ctx, tip, lim)
	if err != nil {
		return err
	}
	var links []link
	for _, e := range entries {
		if e.mode != modeSymlink {
			continue
		}
		m, ok := rules.Match(e.path)
		if !ok {
			m, ok = rules.Match(e.path + "/")
		}
		if ok {
			links = append(links, link{path: e.path, pattern: m.Pattern, reason: m.Reason, id: e.id})
		}
	}
	if len(links) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, l := range links {
		if !seen[l.id] {
			seen[l.id] = true
			ids = append(ids, l.id)
		}
	}
	texts, err := r.linkTargets(ctx, ids)
	if err != nil {
		return err
	}
	index := make(map[string]int, len(changes))
	for i, ch := range changes {
		index[ch.Path] = i
	}
	byTarget := map[string][]link{}
	for _, l := range links {
		text, readable := texts[l.id]
		var resolved, problem string
		if readable {
			resolved, problem = resolveLink(l.path, text)
		} else {
			problem = "its target cannot be read or is longer than 4096 bytes"
		}
		if problem != "" {
			if i, changed := index[l.path]; changed {
				changes[i].Flags = addFlag(changes[i].Flags, Flag{
					Kind: FlagAliased, Path: l.path, Pattern: l.pattern,
					Reason: fmt.Sprintf("the symlink %s (%s) cannot be followed inside the repository: %s",
						leadtext.Escape(l.path), l.reason, problem),
				})
			}
			continue
		}
		l.target = resolved
		key := lowerASCII(resolved)
		byTarget[key] = append(byTarget[key], l)
	}
	if len(byTarget) == 0 {
		return nil
	}
	for i := range changes {
		path := changes[i].Path
		low := lowerASCII(path)
		// The root stands for every path, then every folder above the path
		// and the path itself.
		keys := []string{""}
		for j := 0; j < len(low); j++ {
			if low[j] == '/' {
				keys = append(keys, low[:j])
			}
		}
		keys = append(keys, low)
		for _, k := range keys {
			for _, l := range byTarget[k] {
				if l.path == path {
					continue
				}
				target := l.target
				if target == "" {
					target = "."
				}
				changes[i].Flags = addFlag(changes[i].Flags, Flag{
					Kind: FlagAliased, Path: path, Pattern: l.pattern,
					Reason: fmt.Sprintf("the symlink %s (%s) points at %s, so this path is also reached as that execution-config path",
						leadtext.Escape(l.path), l.reason, leadtext.Escape(target)),
				})
			}
		}
	}
	return nil
}
