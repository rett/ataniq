package noaicheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	KindPolicy     = "policy"     // a release policy list is empty or missing (--require-policy)
	KindIncomplete = "incomplete" // a check could not cover all it must: the run proves nothing there
	KindUnitLink   = "unit-link"  // a unit link that loops or climbs above the root
)

// Incomplete hit names and details: a walk cut short by its depth limit
// ("HIT incomplete <folder> depth"), an entry whose type cannot be found
// ("HIT incomplete <path> type"), and the run's budget spent
// ("HIT incomplete budget <limit>", the limit one of entries, bytes, time
// or memory).
const (
	DetailDepth = "depth"
	DetailType  = "type"
	PathBudget  = "budget"
)

// findingKinds are the kinds of a hit that is a finding: something the check
// found. Every other kind (policy, incomplete, unreadable, unit-link, and any
// later one) says the check could not cover a place, and makes the run
// incomplete.
var findingKinds = []string{KindBinary, KindTool, KindCredential, KindEnv, KindProcess, KindUnit, KindPort, KindPackage, KindNFT}

// DetailEmpty is the detail of a policy hit for an empty allowlist; a
// release without share/nft.digest has the detail "missing" (DetailMissing).
const DetailEmpty = "empty"

// Hit is one finding: a kind, the path or name it is about and, for some
// kinds, a detail (the variable's name, the matched tool, a reason). None of
// them is ever a file's content or a variable's value.
type Hit struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// Report is the result of a run: the hits, the notes, which are not hits,
// and whether the run was complete: every check ran over all it covers and
// every policy of the release was enforced. A run with a NOTE about a policy
// not enforced is not complete, and neither is one with any hit that is not
// a finding (kind "policy", "incomplete", "unreadable" or "unit-link").
type Report struct {
	Hits     []Hit    `json:"hits"`
	Notes    []string `json:"notes"`
	Complete bool     `json:"complete"`
}

// NotePackagesNotEnforced is the note of an empty package allowlist.
const NotePackagesNotEnforced = "packages not enforced"

// Checker runs the checks over one tree with one release's lists.
type Checker struct {
	// RequirePolicy turns each policy a release leaves unset into a hit of
	// kind "policy" instead of a note or a silent default: an empty
	// units.allow, packages.allow or ports.allow, and no share/nft.digest.
	// The switch always sets it; a person checking by hand may not.
	RequirePolicy bool

	t       *tree
	euid    int // the effective user the check runs as
	lists   *Lists
	hits    []Hit
	seen    map[Hit]bool
	notes   []string
	partial bool // a policy was not enforced
	homes   []string

	binScanned bool
	binHits    []string // paths named like an agent tool
	toolHits   []string // paths named like an installer or runtime
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
	return &Checker{t: t, euid: os.Geteuid(), lists: lists, seen: map[Hit]bool{}}, nil
}

// Close releases the tree.
func (c *Checker) Close() error { return c.t.close() }

// Report returns the hits and notes gathered so far. When the run's budget
// is spent its last hit is "incomplete budget <limit>".
func (c *Checker) Report() Report {
	r := Report{Hits: slices.Clone(c.hits), Notes: slices.Clone(c.notes), Complete: !c.partial}
	if c.t.b.spent != "" {
		r.Hits = append(r.Hits, Hit{Kind: KindIncomplete, Path: PathBudget, Detail: c.t.b.spent})
	}
	for _, h := range r.Hits {
		if !slices.Contains(findingKinds, h.Kind) {
			r.Complete = false
		}
	}
	return r
}

// hit records a hit once. Each counts against the run's memory budget; once
// that is spent no more is kept, and the report ends with the budget hit.
func (c *Checker) hit(kind, rel, detail string) {
	h := Hit{Kind: kind, Path: rel, Detail: detail}
	if c.seen[h] {
		return
	}
	if !c.t.b.hold(int64(len(kind)+len(rel)+len(detail)) + 96) {
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

// notEnforced records a policy of the release that is left unset: with
// RequirePolicy a hit of kind "policy" naming the list, without it the note
// n. Either way the run is not complete.
func (c *Checker) notEnforced(list, detail, n string) {
	c.partial = true
	if c.RequirePolicy {
		c.hit(KindPolicy, list, detail)
		return
	}
	if n != "" {
		c.note(n)
	}
}

// unreadable records a place the check could not look into. A place that is
// not there is not a hit, and neither is a read refused because the budget
// is spent (the report says that once). An entry whose type cannot be
// found is "incomplete <path> type".
func (c *Checker) unreadable(rel string, err error) {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errBudget) {
		return
	}
	if errors.Is(err, errUntyped) {
		c.hit(KindIncomplete, show(clean(rel)), DetailType)
		return
	}
	c.hit(KindUnreadable, show(clean(rel)), describe(err))
}

// walk walks the tree from start (see tree.walk) and reports what keeps it
// from covering the folder: a folder it cannot read is a hit of kind
// "unreadable" (a start that is not there is nothing), and a walk that
// stopped at maxDepth above a folder visit wanted entered is the hit
// "incomplete <start> depth". It returns the resolved start, and the
// start's error.
func (c *Checker) walk(start string, maxDepth int, visit func(e *wentry) bool) (string, error) {
	p, deep, err := c.t.walk(start, maxDepth, visit, c.unreadable)
	if err != nil {
		if p == "" {
			p = clean(start)
		}
		c.unreadable(p, err)
		return p, err
	}
	if deep {
		c.hit(KindIncomplete, show(p), DetailDepth)
	}
	return p, nil
}

// RunAll runs every check of the main command in order: the binaries, the
// tools, the credentials, the environment files, the processes, the enabled
// units, the listening ports, the packages and the firewall digest (against
// listing, nil when the caller passed none).
func (c *Checker) RunAll(listing []byte) Report {
	c.CheckBinaries()
	c.CheckTools()
	c.CheckCredentials()
	c.CheckEnv()
	c.CheckProcesses()
	c.CheckUnits()
	c.CheckPorts()
	c.CheckPackages()
	c.CheckNFT(listing)
	return c.Report()
}

// WriteText writes the report as lines: one "HIT kind path [detail]" per hit,
// one "NOTE text" per note, "RUN complete" or "RUN incomplete", and a final
// "ok" or "FAILED n hit(s)". A field
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
	if r.Complete {
		b.WriteString("RUN complete\n")
	} else {
		b.WriteString("RUN incomplete\n")
	}
	if len(r.Hits) == 0 {
		b.WriteString("ok\n")
	} else {
		fmt.Fprintf(&b, "FAILED %d hit(s)\n", len(r.Hits))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON writes the report as one JSON object with "result" ("ok" or
// "failed"), "complete", the count, the hits and the notes.
func (r Report) WriteJSON(w io.Writer) error {
	out := struct {
		Result   string   `json:"result"`
		Complete bool     `json:"complete"`
		Count    int      `json:"count"`
		Hits     []Hit    `json:"hits"`
		Notes    []string `json:"notes"`
	}{"ok", r.Complete, len(r.Hits), r.Hits, r.Notes}
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
// "/", root, and every folder in home/, each resolved and listed once. A
// home inside the kernel's own trees (proc/, sys/, dev/), such as a service
// user's "/proc", is not a place files are put and is not walked.
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
	c.walk("home", 1, func(e *wentry) bool {
		cands = append(cands, e.rel)
		return false
	})
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
		if r == "." || seen[r] || kernelTree(r) {
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

// kernelTree reports whether rel is in proc/, sys/ or dev/.
func kernelTree(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return first == "proc" || first == "sys" || first == "dev"
}
