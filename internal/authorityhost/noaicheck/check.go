package noaicheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
)

// The kinds of a hit.
const (
	KindBinary     = "binary"     // a file named like an agent tool
	KindTool       = "tool"       // an installer or runtime the host never needs
	KindCredential = "credential" // an agent tool's credential or config path
	KindEnv        = "env"        // a provider-key variable set in a file
	KindProcess    = "process"    // a running process named like an agent tool
	KindUnit       = "unit"       // an enabled unit not in units.allow
	KindPort       = "port"       // a listening TCP port not in ports.allow
	KindPackage    = "package"    // a package forbidden or not in packages.allow
	KindNFT        = "nft"        // the firewall rules differ from the release's
	KindUnreadable = "unreadable" // a place the check must look into but cannot
)

// Hit is one finding: a kind, the path or name it is about and, for some
// kinds, a detail (the variable's name, the matched tool, a reason). None of
// them is ever a file's content or a variable's value.
type Hit struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// Report is the result of a run: the hits and the notes, which are not hits.
type Report struct {
	Hits  []Hit    `json:"hits"`
	Notes []string `json:"notes"`
}

// NotePackagesNotEnforced is the note of an empty package allowlist.
const NotePackagesNotEnforced = "packages not enforced"

// Checker runs the checks over one tree with one release's lists.
type Checker struct {
	t     *tree
	lists *Lists
	hits  []Hit
	seen  map[Hit]bool
	notes []string
	homes []string
	bins  []entry
}

// Open opens the tree beneath rootDir for the checks; close it with Close.
func Open(rootDir string, lists *Lists) (*Checker, error) {
	if lists == nil {
		return nil, errors.New("no lists")
	}
	t, err := openTree(rootDir)
	if err != nil {
		return nil, err
	}
	return &Checker{t: t, lists: lists, seen: map[Hit]bool{}}, nil
}

// Close releases the tree.
func (c *Checker) Close() error { return c.t.close() }

// Report returns the hits and notes gathered so far.
func (c *Checker) Report() Report {
	return Report{Hits: slices.Clone(c.hits), Notes: slices.Clone(c.notes)}
}

func (c *Checker) hit(kind, rel, detail string) {
	h := Hit{Kind: kind, Path: rel, Detail: detail}
	if c.seen[h] {
		return
	}
	c.seen[h] = true
	c.hits = append(c.hits, h)
}

func (c *Checker) note(n string) {
	if !slices.Contains(c.notes, n) {
		c.notes = append(c.notes, n)
	}
}

// unreadable records a place the check could not look into. A place that is
// not there is not a hit.
func (c *Checker) unreadable(rel string, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	c.hit(KindUnreadable, show(clean(rel)), describe(err))
}

// RunAll runs every check of the main command in order: the binaries, the
// tools and the credentials.
func (c *Checker) RunAll() Report {
	c.CheckBinaries()
	c.CheckTools()
	c.CheckCredentials()
	return c.Report()
}

// WriteText writes the report as lines: one "HIT kind path [detail]" per hit,
// one "NOTE text" per note, and a final "ok" or "FAILED n hit(s)". A field
// that holds anything but printable ASCII (a space, a control byte, any
// other Unicode), a quote or a backslash is written quoted with Go's ASCII
// escapes, so that a planted name cannot forge a line or the terminal.
func (r Report) WriteText(w io.Writer) error {
	var b strings.Builder
	for _, h := range r.Hits {
		b.WriteString("HIT " + field(h.Kind) + " " + field(h.Path))
		if h.Detail != "" {
			b.WriteString(" " + field(h.Detail))
		}
		b.WriteByte('\n')
	}
	for _, n := range r.Notes {
		b.WriteString("NOTE " + n + "\n")
	}
	if len(r.Hits) == 0 {
		b.WriteString("ok\n")
	} else {
		fmt.Fprintf(&b, "FAILED %d hit(s)\n", len(r.Hits))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON writes the report as one JSON object with the hits, the notes,
// the count and "result" ("ok" or "failed").
func (r Report) WriteJSON(w io.Writer) error {
	out := struct {
		Result string   `json:"result"`
		Count  int      `json:"count"`
		Hits   []Hit    `json:"hits"`
		Notes  []string `json:"notes"`
	}{"ok", len(r.Hits), r.Hits, r.Notes}
	if out.Hits == nil {
		out.Hits = []Hit{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if len(r.Hits) > 0 {
		out.Result = "failed"
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

func field(s string) string {
	if s == "" {
		return `""`
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b <= ' ' || b >= 0x7f || b == '"' || b == '\\' {
			return strconv.QuoteToASCII(s)
		}
	}
	return s
}

// homes are the home folders to look into: every home in etc/passwd except
// "/", root, and every folder in home/, each resolved and listed once.
func (c *Checker) homeFolders() []string {
	if c.homes != nil {
		return c.homes
	}
	var cands []string
	if _, data, err := c.t.readFile("etc/passwd", 16<<20); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Split(line, ":")
			if len(f) < 7 || !strings.HasPrefix(f[5], "/") {
				continue
			}
			cands = append(cands, f[5])
		}
	} else {
		c.unreadable("etc/passwd", err)
	}
	cands = append(cands, "/root")
	p, entries, err := c.t.readDir("home")
	if err != nil {
		c.unreadable("home", err)
	}
	for _, e := range entries {
		cands = append(cands, path.Join(p, e.Name()))
	}
	seen := map[string]bool{}
	c.homes = []string{}
	for _, h := range cands {
		rel := clean(h)
		if rel == "." {
			continue
		}
		r, err := c.t.resolve(rel)
		if err != nil {
			c.unreadable(rel, err)
			continue
		}
		if r == "." || seen[r] {
			continue
		}
		_, fi, err := c.t.lstat(r)
		if err != nil || !fi.IsDir() {
			continue
		}
		seen[r] = true
		c.homes = append(c.homes, r)
	}
	return c.homes
}
