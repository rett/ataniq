package noaicheck

import (
	"errors"
	"io/fs"
	"net/netip"
	"path"
	"strings"
	"syscall"
)

// The hits of the files a Caddyfile imports (see followImports).
const (
	// KindProxy is the kind of a hit for a reverse_proxy target of an
	// imported file that is neither a loopback address, "localhost", a bare
	// port (Caddy's localhost) nor a Unix socket, or that a placeholder
	// stands for: Caddy would call out from the host, or be steered, by a
	// file the release does not hold. "HIT proxy <file> outbound-target".
	KindProxy = "proxy"
	// DetailOutbound is the fixed detail of that hit, never the target.
	DetailOutbound = "outbound-target"
	// PathCaddyImport is the name of the incomplete hit for a file a
	// Caddyfile imports by an absolute path (or one beside an imported
	// file) that is not there: "HIT incomplete caddy-import <path>". Caddy
	// would refuse to start, and the check cannot know what the file would
	// hold once it is there.
	PathCaddyImport = "caddy-import"
)

// maxImportDepth bounds how deep imports of imports are followed; Caddy's
// own use nests a few levels at most. A file past it is
// "incomplete caddy-import <file>".
const maxImportDepth = 8

// importScan follows the files that the Caddyfiles of a release import,
// each once.
type importScan struct {
	c    *Checker
	done map[string]bool
}

// followImports reads the "import" lines of the Caddyfile text data (shown
// as shown in a hit) and reads each file they name on the host, which is
// owner-local like an EnvironmentFile= (the check reads it where the host
// loads it): an absolute path is followed, with a glob in its last
// component matched within the root; a word with no path separator and no
// glob character is a snippet's name and is passed over; a word with a dot
// or glob character and no separator is a file beside the importing one,
// followed only for a file of the host (hostDir is its folder; "" for a
// file of the release, whose folder is read whole already); anything
// else, a placeholder or a relative path, is "incomplete release-syntax
// <file>", never passed over. Each file read is scanned as a release's
// front-end file is (placeholders named like a provider key, see
// releaseRefs) and for outbound reverse_proxy targets (see
// caddyProxyTargets), and its own imports are followed in turn. An
// imported file that is not there is "incomplete caddy-import <path>".
func (s *importScan) followImports(shown, data, hostDir string, depth int) {
	c := s.c
	for _, arg := range caddyImports(data) {
		var rels []string
		switch {
		case strings.ContainsAny(arg, "{}\x00") || arg == "":
			c.hit(KindIncomplete, PathReleaseSyntax, shown)
			continue
		case strings.HasPrefix(arg, "/"):
			if !c.envFileGlob(arg, func(rel string) { rels = append(rels, rel) }) {
				c.hit(KindIncomplete, PathReleaseSyntax, shown)
				continue
			}
		case !strings.Contains(arg, "/"):
			if !strings.ContainsAny(arg, ".*?[") || hostDir == "" {
				continue // a snippet's name, or a file of the release's own folder
			}
			if !c.envFileGlob(path.Join(hostDir, arg), func(rel string) { rels = append(rels, rel) }) {
				c.hit(KindIncomplete, PathReleaseSyntax, shown)
				continue
			}
		default:
			c.hit(KindIncomplete, PathReleaseSyntax, shown)
			continue
		}
		for _, rel := range rels {
			s.file(rel, depth+1)
		}
	}
}

// file reads the imported file rel of the root, once, and scans it.
func (s *importScan) file(rel string, depth int) {
	c := s.c
	p, err := c.t.resolve(rel)
	if err == nil && p == "dev/null" {
		return // a mask, which is empty
	}
	var data []byte
	if err == nil {
		if s.done[p] {
			return
		}
		if !c.t.b.entry() {
			return
		}
		s.done[p] = true
		_, data, err = c.t.readFile(p, maxEnvFile)
	}
	switch {
	case err == nil:
	case errors.Is(err, errBudget):
		return
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		c.hit(KindIncomplete, PathCaddyImport, show(clean(rel)))
		return
	default:
		c.unreadable(rel, err)
		return
	}
	shown := show(p)
	names, bad := releaseRefs(p, string(data))
	for _, n := range names {
		c.hit(KindEnv, shown, n)
	}
	if bad {
		c.hit(KindIncomplete, PathReleaseSyntax, shown)
	}
	for _, t := range caddyProxyTargets(string(data)) {
		if !localProxyTarget(t) {
			c.hit(KindProxy, shown, DetailOutbound)
		}
	}
	if depth >= maxImportDepth {
		if len(caddyImports(string(data))) > 0 {
			c.hit(KindIncomplete, PathCaddyImport, shown)
		}
		return
	}
	s.followImports(shown, string(data), path.Dir(p), depth)
}

// caddyWords splits Caddyfile text into its lines of words. A word is
// quoted with '"' (a backslash escapes in it) or '`'; a '#' that starts a
// word outside quotes is a comment to the end of the line. A quote may
// span lines; its text stays in the word. The result is never a file's
// text in a hit, only what the scans below read from it.
func caddyWords(data string) [][]string {
	var lines [][]string
	var words []string
	var cur strings.Builder
	has := false
	var quote byte
	flush := func() {
		if has {
			words = append(words, cur.String())
			cur.Reset()
			has = false
		}
	}
	endLine := func() {
		flush()
		if len(words) > 0 {
			lines = append(lines, words)
		}
		words = nil
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case quote != 0:
			switch {
			case quote == '"' && c == '\\' && i+1 < len(data):
				i++
				cur.WriteByte(data[i])
			case c == quote:
				quote = 0
			default:
				cur.WriteByte(c)
			}
		case (c == '"' || c == '`') && !has:
			quote, has = c, true
		case c == '#' && !has:
			for i+1 < len(data) && data[i+1] != '\n' {
				i++
			}
		case c == '\n':
			endLine()
		case c == ' ' || c == '\t' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	endLine()
	return lines
}

// caddyImports returns the first argument of every "import" directive of
// the Caddyfile text data (a line whose first word is import).
func caddyImports(data string) []string {
	var out []string
	for _, w := range caddyWords(data) {
		if w[0] == "import" {
			if len(w) < 2 {
				out = append(out, "")
				continue
			}
			out = append(out, w[1])
		}
	}
	return out
}

// caddyProxyTargets returns the upstream addresses a Caddyfile text
// names: the operands of a reverse_proxy line (a leading matcher, a word
// that starts with '/', '*' or '@', is not one), those of a "to" line in
// its block, and "{}" for a "dynamic" line, whose upstreams are found at
// run time and cannot be read here.
func caddyProxyTargets(data string) []string {
	var out []string
	depth := 0
	proxyDepth := -1 // the block depth of an open reverse_proxy block
	for _, w := range caddyWords(data) {
		isProxy := w[0] == "reverse_proxy"
		if isProxy || (proxyDepth >= 0 && depth == proxyDepth+1 && w[0] == "to") {
			args := w[1:]
			if isProxy && len(args) > 0 && args[0] != "" && strings.ContainsAny(args[0][:1], "/*@") {
				args = args[1:]
			}
			for _, a := range args {
				if a == "{" {
					break
				}
				out = append(out, a)
			}
		} else if proxyDepth >= 0 && depth == proxyDepth+1 && w[0] == "dynamic" {
			out = append(out, "{}")
		}
		for i, a := range w {
			switch a {
			case "{":
				if isProxy && i == len(w)-1 {
					proxyDepth = depth
				}
				depth++
			case "}":
				depth--
				if depth <= proxyDepth {
					proxyDepth = -1
				}
			}
		}
	}
	return out
}

// localProxyTarget reports whether the reverse_proxy upstream t stays on
// the host: a Unix socket ("unix//PATH", "unix+h2c//PATH"), "localhost", a
// loopback IP literal, or a bare port (":8080", Caddy's localhost), with
// or without a scheme (http://, https://, h2c://) and a path. A target
// with a placeholder is not local: where it leads is not known here.
func localProxyTarget(t string) bool {
	if t == "" || strings.ContainsAny(t, "{}") {
		return false
	}
	if strings.HasPrefix(t, "unix/") || strings.HasPrefix(t, "unix+h2c/") {
		return true
	}
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	if i := strings.IndexByte(t, '/'); i >= 0 {
		t = t[:i]
	}
	if strings.HasPrefix(t, ":") {
		return true
	}
	host := t
	switch {
	case strings.HasPrefix(t, "["):
		if i := strings.IndexByte(t, ']'); i > 0 {
			host = t[1:i]
		}
	case strings.Count(t, ":") == 1:
		host = t[:strings.IndexByte(t, ':')]
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}
