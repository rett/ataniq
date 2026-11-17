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
		nil, capFor(int64(lim.MaxPaths)*bytesPerRecord), sink)
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

// maxLinkSteps is the most symlinks one resolution passes through, the
// symlink being resolved included. A longer chain, or a loop, is not followed.
const maxLinkSteps = 8

// walker follows the symlinks of the tip's tree while it resolves a target.
// It holds no text itself until the caller has loaded it: resolve says which
// blobs it needs.
type walker struct {
	// links maps the path of every symlink of the tip's tree to the id of
	// the blob that holds its target text.
	links map[string]string
	// texts maps a blob id to its text, for the blobs that were read.
	texts map[string]string
	// unread holds the ids that were asked for and cannot be read: not
	// there, not a blob, or longer than maxLinkTarget.
	unread map[string]bool
}

// resolveLink resolves the target text of a symlink at path link, lexically,
// against the symlink's folder inside the repository, and follows no other
// symlink. See [walker.resolve].
func resolveLink(link, target string) (resolved, problem string) {
	w := &walker{links: map[string]string{link: "-"}, texts: map[string]string{"-": target}}
	resolved, problem, _ = w.resolve(link)
	return resolved, problem
}

// cleanTarget returns the text a symlink holds as the system call that made it
// read it, a C string, so it ends at the first NUL. It returns a problem for a
// target that cannot be followed inside the repository.
func cleanTarget(text string) (target, problem string) {
	if i := strings.IndexByte(text, 0); i >= 0 {
		text = text[:i]
	}
	switch {
	case text == "":
		return "", "its target is empty"
	case strings.HasPrefix(text, "/"):
		return "", "its target is an absolute path"
	}
	return text, ""
}

// resolve resolves the symlink at path link to the repository path it stands
// for, with no leading or trailing slash ("" is the repository root). It
// starts at the symlink's folder, walks the target's elements, and follows
// every symlink of the tip's tree that an element names, the last element
// included, up to maxLinkSteps in all. When it cannot, it returns the problem
// as a short text. When it needs the text of a symlink that was not loaded
// yet, it returns the blob ids to load and neither a result nor a problem;
// the caller loads them (and marks the unreadable ones) and asks again.
func (w *walker) resolve(link string) (resolved, problem string, need []string) {
	var cur []string
	if i := strings.LastIndexByte(link, '/'); i >= 0 {
		cur = strings.Split(link[:i], "/")
	}
	// todo holds the elements still to walk. The symlink's own text is the
	// first.
	var todo []string
	steps := 0
	// follow takes the text of the symlink at path p as the next elements to
	// walk. why is "" for the symlink being resolved and a prefix for a
	// symlink on the way.
	follow := func(p string) (problem string, need []string) {
		steps++
		if steps > maxLinkSteps {
			return "it passes through more than 8 symlinks, or loops", nil
		}
		id := w.links[p]
		text, ok := w.texts[id]
		if !ok {
			if !w.unread[id] {
				return "", []string{id}
			}
			problem = "its target cannot be read or is longer than 4096 bytes"
		} else {
			text, problem = cleanTarget(text)
		}
		if problem != "" {
			if p != link {
				problem = "the symlink " + leadtext.Escape(p) + " on its way: " + problem
			}
			return problem, nil
		}
		todo = append(strings.Split(text, "/"), todo...)
		return "", nil
	}
	if problem, need = follow(link); problem != "" || need != nil {
		return "", problem, need
	}
	for len(todo) > 0 {
		e := todo[0]
		todo = todo[1:]
		switch e {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return "", "its target leaves the repository", nil
			}
			cur = cur[:len(cur)-1]
			continue
		}
		cur = append(cur, e)
		p := strings.Join(cur, "/")
		if _, isLink := w.links[p]; !isLink {
			continue
		}
		// The element is a symlink of the tree: its text replaces it.
		cur = cur[:len(cur)-1]
		if problem, need = follow(p); problem != "" || need != nil {
			return "", problem, need
		}
	}
	return strings.Join(cur, "/"), "", nil
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

// aliasSet is what the symlinks of the tip's tree stand for: the resolved
// target of each symlink whose own path is execution config, in lower case.
type aliasSet struct {
	byTarget map[string][]link
}

// reaching returns the symlinks that make path an execution-config path
// under another name: those whose target is the path, a folder above it, or
// the repository root. The symlink at the path itself is not one.
func (a *aliasSet) reaching(path string) []link {
	if a == nil || len(a.byTarget) == 0 {
		return nil
	}
	low := lowerASCII(path)
	// The root stands for every path, then every folder above the path and
	// the path itself.
	keys := []string{""}
	for j := 0; j < len(low); j++ {
		if low[j] == '/' {
			keys = append(keys, low[:j])
		}
	}
	keys = append(keys, low)
	var out []link
	for _, k := range keys {
		for _, l := range a.byTarget[k] {
			if l.path != path {
				out = append(out, l)
			}
		}
	}
	return out
}

// targetText is how a flag names the target of a link.
func (l link) targetText() string {
	if l.target == "" {
		return "."
	}
	return l.target
}

// aliasFlags adds the aliased flags to the changes and returns the alias set,
// for the history pass.
//
// A symlink whose own path is execution config makes another folder stand
// for it: a symlink ".claude" that points at "cfg" makes "cfg/settings.json"
// agent settings. So every symlink of the tip's tree, changed or not, whose
// own path matches the rules (as a file or as a folder, because a link can
// stand for a folder) is resolved by its text, and every changed path that
// is its target or lies below it is flagged. The resolution follows the tip's
// own symlinks that the target passes through, up to maxLinkSteps. A symlink
// whose target is empty, absolute, out of the repository, unreadable, or
// reached through a longer chain or a loop is flagged on its own path, when
// that path is changed.
func (r *runner) aliasFlags(ctx context.Context, rules execconfig.Set, tip string, changes []Change, lim Limits) (*aliasSet, error) {
	entries, err := r.treeEntries(ctx, tip, lim)
	if err != nil {
		return nil, err
	}
	w := &walker{links: map[string]string{}, texts: map[string]string{}, unread: map[string]bool{}}
	var links []link
	for _, e := range entries {
		if e.mode != modeSymlink {
			continue
		}
		w.links[e.path] = e.id
		m, ok := rules.Match(e.path)
		if !ok {
			m, ok = rules.Match(e.path + "/")
		}
		if ok {
			links = append(links, link{path: e.path, pattern: m.Pattern, reason: m.Reason, id: e.id})
		}
	}
	if len(links) == 0 {
		return nil, nil
	}
	index := make(map[string]int, len(changes))
	for i, ch := range changes {
		index[ch.Path] = i
	}
	set := &aliasSet{byTarget: map[string][]link{}}
	// Each round resolves what it can and loads the texts the rest needs. A
	// resolution needs at most maxLinkSteps texts, so the rounds end.
	pending := links
	for len(pending) > 0 {
		var again []link
		want := map[string]bool{}
		var ids []string
		for _, l := range pending {
			resolved, problem, need := w.resolve(l.path)
			if len(need) > 0 {
				again = append(again, l)
				for _, id := range need {
					if !want[id] {
						want[id] = true
						ids = append(ids, id)
					}
				}
				continue
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
			set.byTarget[key] = append(set.byTarget[key], l)
		}
		if len(ids) > 0 {
			texts, err := r.linkTargets(ctx, ids)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if text, ok := texts[id]; ok {
					w.texts[id] = text
				} else {
					w.unread[id] = true
				}
			}
		}
		pending = again
	}
	for i := range changes {
		for _, l := range set.reaching(changes[i].Path) {
			changes[i].Flags = addFlag(changes[i].Flags, Flag{
				Kind: FlagAliased, Path: changes[i].Path, Pattern: l.pattern,
				Reason: fmt.Sprintf("the symlink %s (%s) points at %s, so this path is also reached as that execution-config path",
					leadtext.Escape(l.path), l.reason, leadtext.Escape(l.targetText())),
			})
		}
	}
	return set, nil
}
