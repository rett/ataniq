package noaicheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The phases of a run (see RunPhase).
const (
	PhasePre  = "pre"
	PhasePost = "post"
	PhaseFull = "full"
)

// The folders of a release the release check reads: its units, read like
// installed units, and its front end's and firewall's configuration, read
// for the names of provider-key variables only.
const (
	releaseUnits    = "etc/systemd"
	releaseNameDirs = "etc/caddy etc/nftables"
)

// RunPhase runs the checks of one phase and returns the report:
//
//   - pre, before a switch: the candidate release's own files (see
//     CheckRelease) and the host's binaries, tools, credentials,
//     environment files, processes, enabled units and packages; not the
//     listening ports and not the firewall digest, which describe the
//     services the switch has not started yet (with RequirePolicy an
//     empty ports.allow and a missing nft.digest are still hits: the
//     release must carry them);
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
	c.CheckUnits()
	if phase == PhasePre {
		if len(c.lists.PortsAllow) == 0 && c.RequirePolicy {
			c.notEnforced(FilePortsAllow, DetailEmpty, "")
		}
	} else {
		c.CheckPorts()
	}
	c.CheckPackages()
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
// (Environment=, PassEnvironment= and EnvironmentFile= by name; a link to
// /dev/null is a mask and is not read; a file in a folder named "user" is
// a user unit, its EnvironmentFile= resolved in every user's context, any
// other a system unit), and every file a unit's EnvironmentFile= names is
// read on the host like any other. Every file beneath its etc/caddy and
// etc/nftables is read for names only: each word named like a provider key
// (see ProviderKeyName) is a hit of kind "env". A hit names the file by
// its path within the root when the release is beneath it, by its own path
// otherwise. A release folder that cannot be opened is an error.
func (c *Checker) CheckRelease(dir string) error {
	r, err := os.OpenRoot(dir)
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
			target, err := rt.readlink(e.rel)
			if err == nil && path.Clean(target) == "/dev/null" {
				return "", nil, false // a mask
			}
			p, err = rt.resolveStrict(e.rel)
			switch {
			case errors.Is(err, errLoop):
				c.hit(KindUnitLink, at(e.rel), DetailLoop)
				return "", nil, false
			case errors.Is(err, errEscape):
				c.hit(KindUnitLink, at(e.rel), DetailEscape)
				return "", nil, false
			case err != nil:
				fail(e.rel, err)
				return "", nil, false
			}
		} else if !e.isRegular() {
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
	walk(releaseUnits, func(e *wentry) {
		p, data, ok := read(e)
		if !ok {
			return
		}
		u := parseEnv(syntaxSystemd, string(data), false)
		for _, n := range u.names {
			c.hit(KindEnv, at(p), n)
		}
		if u.bad {
			c.hit(KindIncomplete, PathEnvSyntax, at(p))
		}
		managers := []manager{{}}
		if userUnitPath(p) {
			managers = c.managers()[1:]
		}
		for _, m := range managers {
			s.envFiles(at(p), u.files, m)
		}
	})
	for _, d := range strings.Fields(releaseNameDirs) {
		walk(d, func(e *wentry) {
			p, data, ok := read(e)
			if !ok {
				return
			}
			for _, n := range providerWords(string(data)) {
				c.hit(KindEnv, at(p), n)
			}
		})
	}
	s.drain()
	return nil
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

// providerWords returns the words of data named like a provider key, once
// each: every run of identifier bytes is a word.
func providerWords(data string) []string {
	var r envParse
	for i := 0; i < len(data); {
		if !identStart(data[i]) || (i > 0 && ident(data[i-1])) {
			i++
			continue
		}
		j := i
		for j < len(data) && ident(data[j]) {
			j++
		}
		r.add(data[i:j])
		i = j
	}
	return r.names
}
