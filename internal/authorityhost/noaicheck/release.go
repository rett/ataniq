package noaicheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// The phases of a run (see RunPhase).
const (
	PhasePre  = "pre"
	PhasePost = "post"
	PhaseFull = "full"
)

// The folders of a release the release check reads: its units, read like
// installed units, and its front end's configuration, read for the
// variables it refers to by name only. The firewall's configuration
// (etc/nftables) refers to no environment variable and is not read for
// names.
const (
	releaseUnits    = "etc/systemd"
	releaseNameDirs = "etc/caddy"
)

// PathReleaseSyntax is the name of the incomplete hit for a front-end
// configuration file holding a variable reference the check cannot read
// ("{env." or, in a Caddyfile, "{$" not followed by a name and its
// closing brace) or, for Caddy's JSON, JSON it cannot parse:
// "HIT incomplete release-syntax <file>". The hit names the file and a
// fixed reason, nothing of its content.
const PathReleaseSyntax = "release-syntax"

// RunPhase runs the checks of one phase and returns the report:
//
//   - pre, before a switch: the candidate release's own files (see
//     CheckRelease) and the host's binaries, tools, credentials,
//     environment files, processes, enabled units and forbidden
//     packages; not the listening ports and not the firewall digest,
//     which describe the services the switch has not started yet, and
//     not the installed packages against packages.allow, which the
//     switch's package step changes after this phase (with
//     RequirePolicy an empty ports.allow, packages.allow and a missing
//     nft.digest are still hits: the release must carry them);
//   - post, after a switch: all of pre, and the listening ports and the
//     firewall digest against listing;
//   - full, the default, a check of the host as it is: every check of the
//     host with the ports and the digest, and no separate read of the
//     release's files (its units are read where the host loads them).
//
// release is the release folder; its etc/ is read in pre and post. An
// unknown phase, or a release folder that cannot be opened in pre or post,
// is an error and no report.
func (c *Checker) RunPhase(phase, release string, listing []byte) (Report, error) {
	switch phase {
	case PhasePre, PhasePost:
		if err := c.CheckRelease(release); err != nil {
			return Report{}, err
		}
	case PhaseFull:
	default:
		return Report{}, fmt.Errorf("unknown phase %s", Field(phase))
	}
	c.CheckBinaries()
	c.CheckTools()
	c.CheckCredentials()
	c.CheckEnv()
	c.CheckProcesses()
	c.checkUnits(phase == PhasePost && c.witnessRelease)
	if phase == PhasePre {
		if len(c.lists.PortsAllow) == 0 && c.RequirePolicy {
			c.notEnforced(FilePortsAllow, DetailEmpty, "")
		}
	} else {
		c.CheckPorts()
	}
	c.checkPackages(phase != PhasePre)
	if phase == PhasePre {
		if c.lists.NFTDigest == "" && c.RequirePolicy {
			c.notEnforced(FileNFTDigest, DetailMissing, "")
		}
	} else {
		c.CheckNFT(listing)
	}
	r := c.Report()
	r.Phase = phase
	return r, nil
}

// CheckRelease reads the release folder dir's own files, as the release
// would install them, before the host loads any: every file beneath its
// etc/systemd is a unit or drop-in, read with the rules of installed units
// (Environment=, PassEnvironment= and EnvironmentFile= by name; all symlinks anywhere in the release are refused; a file in a folder named "user" is
// a user unit, its EnvironmentFile= resolved in every user's context, any
// other a system unit), and every file a unit's EnvironmentFile= names is
// read on the host like any other. Every file beneath its etc/caddy is
// read, by its format, for the variables it refers to through Caddy's own
// placeholders (see releaseRefs): each one named like a provider key (see
// ProviderKeyName) is a hit of kind "env", and a reference the check
// cannot read is "incomplete release-syntax <file>". A Caddyfile's
// "import" of a file of the host (an absolute path) is followed, and the
// file read where the host holds it, as an owner-local file is: see
// importScan.followImports; it is scanned as the release's own files are
// and for outbound reverse_proxy targets, and one that is not there is
// "incomplete caddy-import <path>".
// A hit names the file by its path within the root when the release is
// beneath it, by its own path otherwise. A release folder that cannot be
// opened is an error.
func (c *Checker) CheckRelease(dir string) error {
	r, err := openRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	rt := &tree{r: r, b: c.t.b}
	disp := c.releaseDisplay(dir)
	at := func(rel string) string { return disp + "/" + clean(rel) }
	fail := func(rel string, err error) {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, errBudget):
		case errors.Is(err, errUntyped):
			c.hit(KindIncomplete, at(rel), DetailType)
		default:
			c.hit(KindUnreadable, at(rel), describe(err))
		}
	}
	walk := func(start string, visit func(e *wentry)) {
		_, deep, err := rt.walk(start, treeDepth, func(e *wentry) bool {
			if e.isDir() {
				return true
			}
			visit(e)
			return false
		}, fail)
		if err != nil {
			fail(start, err)
		}
		if deep {
			c.hit(KindIncomplete, at(start), DetailDepth)
		}
	}
	read := func(e *wentry) (string, []byte, bool) {
		p := e.rel
		if e.isLink() {
			return "", nil, false // The whole-tree walk reports every release link.
		}
		if !e.isRegular() {
			fail(e.rel, errNotRegular)
			return "", nil, false
		}
		_, data, err := rt.readFile(p, maxEnvFile)
		if err != nil {
			fail(p, err)
			return "", nil, false
		}
		return p, data, true
	}
	s := c.newEnvScan()
	units := map[string]*candUnit{}
	var order []*candUnit
	_, _, binErr := rt.lstat("bin/ataniq-witness")
	hasWitness := binErr == nil
	c.witnessRelease = hasWitness
	if binErr != nil && !errors.Is(binErr, fs.ErrNotExist) {
		fail("bin/ataniq-witness", binErr)
	}
	seenWitness := false
	relDir := "/opt/authority-host/releases/" + filepath.Base(filepath.Clean(dir))
	shipped := func(name string) bool {
		rel := "bin/" + name
		resolved, info, err := rt.lstat(rel)
		if err != nil || resolved != rel || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return false
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		return ok && st.Nlink == 1
	}
	walk(".", func(e *wentry) {
		if e.isLink() {
			c.hit(KindIncomplete, at(e.rel), "release-symlink")
			return
		}
		if !strings.HasPrefix(e.rel, releaseUnits+"/") {
			return
		}
		p, data, ok := read(e)
		if !ok {
			return
		}
		if e.rel == WitnessUnit {
			seenWitness = true
			if !hasWitness {
				c.hit(KindUnit, at(e.rel), "witness-binary")
			}
		}
		text := string(data)
		if !UnitTextSafe(text) {
			c.hit(KindUnit, at(p), UnitSyntax)
			return
		}
		if !ReleaseExecsSafe(text, relDir, shipped) {
			c.hit(KindUnit, at(e.rel), "decision-exec")
		}
		if (hasWitness || e.rel == WitnessUnit) && !WitnessUnitSafe(e.rel, text, relDir) {
			c.hit(KindUnit, at(e.rel), "witness-isolation")
		}
		u := parseEnv(syntaxSystemd, text, false)
		for _, n := range u.names {
			c.hit(KindEnv, at(p), n)
		}
		if u.bad {
			c.hit(KindIncomplete, PathEnvSyntax, at(p))
		}
		dir, name, dropin := releaseUnitLocation(e.rel)
		key := strconv.FormatBool(userUnitPath(p)) + "\x00" + dir + "\x00" + name
		ru := units[key]
		if ru == nil {
			if !c.t.b.hold(int64(len(key)) + 96) {
				return
			}
			ru = &candUnit{user: userUnitPath(p)}
			units[key] = ru
			order = append(order, ru)
		}
		// Only what the merge needs is kept, charged before it is kept:
		// the file's EnvironmentFile= lines and whether one resets them.
		kept := envParse{reset: u.reset, files: slices.Clone(u.files)}
		if cost := kept.cost(); c.t.b.hold(cost) {
			ru.held += cost
		} else {
			return
		}
		f := releaseFile{shown: at(p), r: &kept, name: path.Base(e.rel)}
		if dropin {
			ru.dropins = append(ru.dropins, f)
		} else {
			ru.main = &f
		}
	})
	if hasWitness && !seenWitness {
		c.hit(KindUnit, at(WitnessUnit), "witness-unit-missing")
	}
	// Each unit's lines are evaluated as systemd merges them, the unit
	// file and then its drop-ins by file name, before any file they name
	// is opened: an empty EnvironmentFile= drops those before it. The
	// units go one at a time: a unit's merged references are charged
	// before they are built, and its files' data and they are released
	// once the files they name are queued; no unit is checked once the
	// budget is spent.
	for _, ru := range order {
		if c.t.b.out() {
			break
		}
		c.checkCandUnit(s, ru)
		c.t.b.release(ru.held)
		ru.held, ru.main, ru.dropins = 0, nil, nil
	}
	imports := &importScan{c: c, done: map[string]bool{}}
	for _, d := range strings.Fields(releaseNameDirs) {
		if resolved, err := rt.resolve(d); err == nil && resolved != d {
			continue // The link to this folder was already refused.
		}
		walk(d, func(e *wentry) {
			p, data, ok := read(e)
			if !ok {
				return
			}
			names, bad := releaseRefs(p, string(data))
			for _, n := range names {
				c.hit(KindEnv, at(p), n)
			}
			if bad {
				c.hit(KindIncomplete, PathReleaseSyntax, at(p))
			}
			if !strings.HasSuffix(p, ".json") {
				imports.followImports(at(p), string(data), "", 0)
			}
		})
	}
	s.drain()
	return nil
}

// candUnit is a unit of the candidate release in one folder: its unit
// file and the drop-ins of its drop-in folder beside it, and whether it is
// a user unit. A unit of the same name in another folder of the release
// is another candUnit, read on its own: nothing it sets is passed over.
type candUnit struct {
	user    bool
	main    *releaseFile
	dropins []releaseFile
	held    int64 // what its files' kept data holds of the budget
}

// unitRefCost is the memory a merged EnvironmentFile= reference holds.
const unitRefCost = 64

// checkCandUnit merges the lines of the candidate unit ru as systemd does
// and queues the files they name, for every manager that loads it (see
// CheckRelease). The merged references are charged first, at most one per
// line of its files, and released when it is done.
func (c *Checker) checkCandUnit(s *envScan, ru *candUnit) {
	n := 0
	if ru.main != nil {
		n += len(ru.main.r.files)
	}
	for _, d := range ru.dropins {
		n += len(d.r.files)
	}
	held := int64(n) * unitRefCost
	if !c.t.b.hold(held) {
		return
	}
	defer c.t.b.release(held)
	refs := make([]unitRef, 0, n)
	if ru.main != nil {
		refs = appendUnitRefs(refs, ru.main.shown, ru.main.r)
	}
	slices.SortStableFunc(ru.dropins, func(a, b releaseFile) int { return strings.Compare(a.name, b.name) })
	for _, d := range ru.dropins {
		refs = appendUnitRefs(refs, d.shown, d.r)
	}
	managers := []manager{{}}
	if ru.user {
		managers = c.managers()[1:]
	}
	for _, m := range managers {
		for _, x := range refs {
			if c.t.b.out() {
				return
			}
			s.envFiles(x.unit, []envRef{x.envRef}, m)
		}
	}
}

// releaseFile is one file of a candidate unit, as shown in a hit, and
// what it sets.
type releaseFile struct {
	shown string
	name  string // its file name
	r     *envParse
}

// ReleaseUnitName maps a shipped systemd file to its unit name. A .conf
// drop-in names the unit alongside its directory. A unit file names itself.
func ReleaseUnitName(rel string) string {
	if strings.HasSuffix(rel, ".conf") {
		_, name, _ := releaseUnitLocation(rel)
		return name
	}
	return path.Base(rel)
}

// releaseUnitLocation returns the unit the release file rel (where it sits,
// not where a link leads) belongs to, and the folder holding that unit:
// the unit of its folder and the folder above, and true, when its folder
// is a drop-in folder ("app.service.d"); its own name and folder
// otherwise. A type's or a prefix's drop-in folder ("service.d") is a
// unit of its own, as for installed units.
func releaseUnitLocation(rel string) (dir, name string, dropin bool) {
	folder := path.Dir(rel)
	if base := path.Base(folder); isDropinDir(base) && folder != "." {
		return path.Dir(folder), strings.TrimSuffix(base, ".d"), true
	}
	return folder, path.Base(rel), false
}

// userUnitPath reports whether the release file rel is a user unit: a
// folder on its way is named "user".
func userUnitPath(rel string) bool {
	return strings.Contains("/"+path.Dir(rel)+"/", "/user/")
}

// releaseDisplay is how the hits of CheckRelease name the release folder
// dir: its path within the root when it is beneath it ("/opt/..."), its own
// absolute path otherwise.
func (c *Checker) releaseDisplay(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	abs = filepath.ToSlash(filepath.Clean(abs))
	if rel, err := filepath.Rel(c.rootAbs, filepath.FromSlash(abs)); err == nil && c.rootAbs != "" {
		rel = filepath.ToSlash(rel)
		if rel != ".." && !strings.HasPrefix(rel, "../") {
			return strings.TrimSuffix(show(clean(rel)), "/")
		}
	}
	return strings.TrimSuffix(abs, "/")
}

// releaseRefs returns the variables a front-end configuration file named
// name refers to that are named like a provider key, once each, and
// whether it holds a reference (or syntax) it cannot read. The format is
// the file's: Caddy's native JSON for a name ending in ".json" (see
// caddyJSONRefs), a Caddyfile otherwise (see caddyfileRefs). Only a
// placeholder Caddy itself replaces names a variable: shell-style $NAME
// and ${NAME} mean nothing to Caddy, so they are literal text, never
// matched and never copied (a systemd unit's are read by the unit rules).
// The caller reports a file it cannot read by its name and a fixed
// reason, never its text.
func releaseRefs(name, data string) ([]string, bool) {
	if strings.HasSuffix(name, ".json") {
		return caddyJSONRefs(data)
	}
	return caddyfileRefs(data)
}

// caddyfileRefs reads a Caddyfile for Caddy's placeholders {env.NAME} and
// {$NAME} ({$NAME:default} too). A comment (a '#' that starts a word
// outside quotes, to the end of its line) is passed over, and so is every
// literal operand: a word that merely looks like a variable's name is not
// one. A "{env." or "{$" not followed by a valid name and its closing
// brace (or, for {$, a ':' and a default that is itself closed by a '}')
// is a reference the check cannot read: such a reference adds no name.
func caddyfileRefs(data string) ([]string, bool) {
	var r envParse
	bad := false
	var quote byte
	word := true // at the start of a word: a '#' here starts a comment
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case quote != 0 && c == '\\' && quote == '"':
			i++
			continue
		case quote != 0 && c == quote:
			quote = 0
			continue
		case quote == 0 && (c == '"' || c == '`'):
			quote, word = c, false
			continue
		case quote == 0 && word && c == '#':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			word = true
			continue
		}
		word = false
		rest := data[i:]
		var name, end string
		switch {
		case strings.HasPrefix(rest, "{env."):
			name, end = refName(rest[5:], "}")
		case strings.HasPrefix(rest, "{$"):
			name, end = refName(rest[2:], "}:")
			if name != "" && end[0] == ':' && strings.IndexByte(end, '}') < 0 {
				name = "" // a default without its closing brace
			}
		default:
			continue
		}
		if name == "" {
			bad = true
			continue
		}
		r.add(name)
		i += len(rest) - len(end) - 1
	}
	return r.names, bad
}

// maxJSONDepth bounds the nesting of a Caddy JSON file the check follows;
// Caddy's own configuration nests a few dozen levels at most.
const maxJSONDepth = 256

// caddyJSONRefs reads Caddy's native JSON configuration as JSON, token by
// token, and looks in every string (decoded: a key or a value) for the
// one placeholder Caddy replaces in it that reads the environment,
// {env.NAME}; a brace escaped for Caddy's replacer ("\{") is literal
// text. Everything else, a literal '$' and Caddyfile's {$NAME} included,
// is a literal value: private, never matched. An "{env." not followed by
// a valid name and its closing brace, JSON that does not parse, and JSON
// nested deeper than maxJSONDepth are what the check cannot read.
func caddyJSONRefs(data string) ([]string, bool) {
	var r envParse
	bad := false
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if depth != 0 {
				return r.names, true // cut short
			}
			break
		}
		if err != nil {
			return r.names, true
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
				if depth > maxJSONDepth {
					return r.names, true
				}
			} else {
				depth--
			}
		case string:
			if envPlaceholders(v, &r) {
				bad = true
			}
		}
	}
	return r.names, bad
}

// envPlaceholders adds to r the name of every {env.NAME} placeholder in
// the decoded JSON string s, passing over one whose brace is escaped
// ("\{env."), and reports whether s holds one it cannot read.
func envPlaceholders(s string, r *envParse) bool {
	bad := false
	for i := 0; ; {
		j := strings.Index(s[i:], "{env.")
		if j < 0 {
			return bad
		}
		k := i + j
		i = k + len("{env.")
		if k > 0 && s[k-1] == '\\' {
			continue
		}
		name, _ := refName(s[i:], "}")
		if name == "" {
			bad = true
			continue
		}
		r.add(name)
	}
}

// refName reads the name of a reference at the start of s, up to one of
// the bytes of stops (none: the name ends at the first byte that is not an
// identifier's), and returns it with the rest of s from that stop on; ""
// when there is no valid name, or no stop after it.
func refName(s, stops string) (string, string) {
	j := 0
	for j < len(s) && ident(s[j]) {
		j++
	}
	if !validName(s[:j]) {
		return "", s
	}
	if stops == "" {
		return s[:j], s[j:]
	}
	if j >= len(s) || strings.IndexByte(stops, s[j]) < 0 {
		return "", s
	}
	return s[:j], s[j:]
}
