package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ataniq/internal/authorityhost/switcher"
)

// CheckOptions are the choices of Check.
type CheckOptions struct {
	// NoNoAICheck skips the run of the shipped no-AI check (for tests, and
	// for a host that cannot run it).
	NoNoAICheck bool
	// Root, when not empty, is passed to the no-AI check as --root: the
	// tree whose host it looks at (default /, the host it runs on).
	Root string
	// RequirePolicy passes --require-policy to the no-AI check: the release
	// must then carry its allowlists and nft.digest. The source tree ships
	// the allowlists empty, filled from the host's baseline when it is
	// deployed, so a built folder passes it only after they are filled.
	RequirePolicy bool
	// Timeout bounds the no-AI check; zero is NoAICheckTimeout.
	Timeout time.Duration
}

// MaxFindings is how many findings Check keeps.
const MaxFindings = 1000

type checker struct {
	findings []Finding
	seen     map[Finding]bool
}

func (c *checker) add(code, name string) {
	f := Finding{Code: code, Name: name}
	if c.seen[f] || len(c.findings) >= MaxFindings {
		return
	}
	if c.seen == nil {
		c.seen = map[Finding]bool{}
	}
	c.seen[f] = true
	c.findings = append(c.findings, f)
}

// topLevel are the names a release holds at its top.
var topLevel = map[string]bool{
	switcher.ManifestFile: true, switcher.SumsFile: true, switcher.SignatureFile: true,
	"bin": true, "etc": true, "share": true,
}

// Check reads the built release folder dir back and returns what is wrong
// with it, in the order it found it; an empty result is a pass. It checks
// the folder's name, shape and modes (no link, no special file, nothing
// writable by group or others), SHA256SUMS against the files and in its
// canonical form, the MANIFEST (the switch's own reader, and that it
// describes the folder), the coverage of the command lists (see
// checkCoverage) and, unless NoNoAICheck, the shipped no-AI check.
func Check(dir string, o CheckOptions) []Finding {
	c := &checker{}
	clean := filepath.Clean(dir)
	id, err := switcher.ParseID(filepath.Base(clean))
	if err != nil {
		c.add(CodeFolder, "")
		return c.findings
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() {
		c.add(CodeFolder, "")
		return c.findings
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		c.add(CodeFolder, "")
		return c.findings
	}
	defer root.Close()

	n := 0
	ents, err := walk(root, ".", &n)
	if err != nil {
		c.add(CodeEntryLimit, "")
		return c.findings
	}
	files := c.shape(ents)
	c.sums(root, files)
	m, mok := c.manifest(root, id)
	c.bins(files)
	if mok {
		c.coverage(root, m, id.Name, files)
	}
	if !o.NoNoAICheck {
		if _, ok := files["bin/ah-noaicheck"]; ok {
			c.runNoAICheck(clean, o)
		}
	}
	return c.findings
}

// shape checks every entry (kind, name, links, modes) and returns the
// regular files by path.
func (c *checker) shape(ents []entry) map[string]entry {
	files := map[string]entry{}
	for _, e := range ents {
		if !validPath(e.path) {
			c.add(CodeBadName, "")
			continue
		}
		if top, _, _ := strings.Cut(e.path, "/"); !topLevel[top] {
			c.add(CodeTopLevel, top)
		}
		switch {
		case e.dir():
		case e.regular():
			files[e.path] = e
			if e.nlink > 1 {
				c.add(CodeHardLink, e.path)
			}
		default:
			c.add(CodeNotRegular, e.path)
			continue
		}
		if e.mode&(0o022|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			c.add(CodeUnsafeMode, e.path)
		}
	}
	return files
}

// paths is the paths of files, sorted, so that findings come in one order.
func paths(files map[string]entry) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// sums reads SHA256SUMS with the switch's parser and checks it against the
// files: every file listed and its digest, no file missing, and the exact
// text the builder writes (sorted, one line per file).
func (c *checker) sums(root *os.Root, files map[string]entry) {
	if _, ok := files[switcher.SumsFile]; !ok {
		c.add(CodeSumsMissing, "")
		return
	}
	data, err := readSmall(root, switcher.SumsFile)
	if err != nil {
		c.add(CodeSumsInvalid, "")
		return
	}
	listed, err := switcher.ParseSums(data)
	if err != nil {
		c.add(CodeSumsInvalid, "")
		return
	}
	bad := false
	computed := map[string]string{}
	var total int64
	for _, p := range paths(files) {
		e := files[p]
		if p == switcher.SumsFile || p == switcher.SignatureFile {
			continue
		}
		want, ok := listed[p]
		if !ok {
			c.add(CodeSumsUnlisted, p)
			bad = true
			continue
		}
		if total += e.size; e.size > MaxFileBytes || total > MaxTotalBytes {
			c.add(CodeEntryLimit, "")
			return
		}
		got, err := hashFile(root, p)
		if err != nil {
			c.add(CodeIO, p)
			bad = true
			continue
		}
		computed[p] = got
		if got != want {
			c.add(CodeSumsMismatch, p)
			bad = true
		}
	}
	var gone []string
	for p := range listed {
		if _, ok := files[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		c.add(CodeSumsAbsent, p)
		bad = true
	}
	if !bad && !bytes.Equal(data, sumsText(computed)) {
		c.add(CodeSumsForm, "")
	}
}

func hashFile(root *os.Root, p string) (string, error) {
	f, info, err := openRegular(root, p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, info.Size()+1))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// manifest reads MANIFEST with the switch's own parser and checks that it
// describes the folder id; ok is false when it cannot be used.
func (c *checker) manifest(root *os.Root, id switcher.ReleaseID) (switcher.Manifest, bool) {
	data, err := readSmall(root, switcher.ManifestFile)
	if err != nil {
		c.add(CodeManifest, "")
		return switcher.Manifest{}, false
	}
	m, err := switcher.ParseManifest(data)
	if err != nil || !valuePattern.MatchString(m.BuiltOn) || !valuePattern.MatchString(m.GoVersion) {
		c.add(CodeManifest, "")
		return switcher.Manifest{}, false
	}
	if err := m.Check(id); err != nil {
		c.add(CodeManifestID, "")
		return m, false
	}
	return m, true
}

// bins checks the programs: the two every release needs are there, and
// everything in bin/ has an execute bit.
func (c *checker) bins(files map[string]entry) {
	for _, name := range requiredBins {
		if _, ok := files["bin/"+name]; !ok {
			c.add(CodeBinMissing, "bin/"+name)
		}
	}
	for _, p := range paths(files) {
		if strings.HasPrefix(p, "bin/") && files[p].mode&0o111 == 0 {
			c.add(CodeNotExec, p)
		}
	}
}
