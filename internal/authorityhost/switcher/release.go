package switcher

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The names a release folder holds at its top.
const (
	ManifestFile  = "MANIFEST"
	SumsFile      = "SHA256SUMS"
	SignatureFile = "SHA256SUMS.sig"
)

// maxSmallFile bounds the files the switch reads whole: the sums, the
// manifest and the command lists.
const maxSmallFile = 4 << 20

// ReleaseID is a parsed release name, <counter>-<short commit>.
type ReleaseID struct {
	Name    string
	Counter uint64
	Commit  string
}

var idPattern = regexp.MustCompile(`^([0-9]{4,18})-([0-9a-f]{7,40})$`)

// ParseID parses a release name such as 0007-ab12cd3: a counter of at least
// four digits, zero-padded, a hyphen and the short commit in lowercase hex.
func ParseID(s string) (ReleaseID, error) {
	m := idPattern.FindStringSubmatch(s)
	if m == nil {
		return ReleaseID{}, fmt.Errorf("release id %s is not <counter>-<short commit>, such as 0007-ab12cd3", q(s))
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return ReleaseID{}, fmt.Errorf("release id %s: the counter is out of range", q(s))
	}
	return ReleaseID{Name: s, Counter: n, Commit: m[2]}, nil
}

// Manifest is the MANIFEST of a release.
type Manifest struct {
	ID              string
	Counter         uint64
	Commit          string
	BuiltOn         string
	GoVersion       string
	PackageSnapshot string
	// Components is "static" for a release with no decision services, whose
	// command lists may then hold comments only; empty otherwise.
	Components string
}

// ComponentsStatic is the one value of the optional components key.
const ComponentsStatic = "static"

var manifestKeys = []string{"id", "counter", "commit", "built_on", "go_version", "package_snapshot"}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// snapshotPattern is a package snapshot identifier: what the journal and
// deploy.json carry, bounded so that both always fit their readers.
var snapshotPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// ValidSnapshot reports whether s is a package snapshot identifier: 1 to 128
// bytes of letters, digits and ._:-.
func ValidSnapshot(s string) bool { return snapshotPattern.MatchString(s) }

// ParseManifest reads key: value lines. Blank lines and lines starting with #
// are skipped, an unknown key is ignored, a repeated key or a missing or empty
// required key is an error.
func ParseManifest(data []byte) (Manifest, error) {
	values := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Manifest{}, failf(CodeManifestInvalid, "MANIFEST line %d: not key: value", i+1)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if _, dup := values[key]; dup {
			return Manifest{}, failf(CodeManifestInvalid, "MANIFEST line %d: %s given twice", i+1, q(key))
		}
		values[key] = value
	}
	for _, key := range manifestKeys {
		if values[key] == "" {
			return Manifest{}, failf(CodeManifestInvalid, "MANIFEST: %s missing or empty", key)
		}
	}
	n, err := strconv.ParseUint(values["counter"], 10, 64)
	if err != nil {
		return Manifest{}, failf(CodeManifestInvalid, "MANIFEST: counter is not a number")
	}
	if !ValidSnapshot(values["package_snapshot"]) {
		return Manifest{}, failf(CodeManifestInvalid, "MANIFEST: package_snapshot is not 1 to 128 bytes of letters, digits and ._:-")
	}
	if c, ok := values["components"]; ok && c != ComponentsStatic {
		return Manifest{}, failf(CodeManifestInvalid, "MANIFEST: components %s is not static", q(c))
	}
	return Manifest{
		ID:              values["id"],
		Counter:         n,
		Commit:          values["commit"],
		BuiltOn:         values["built_on"],
		GoVersion:       values["go_version"],
		PackageSnapshot: values["package_snapshot"],
		Components:      values["components"],
	}, nil
}

// Check reports whether the manifest describes the release named id.
func (m Manifest) Check(id ReleaseID) error {
	if m.ID != id.Name {
		return failf(CodeManifestMismatch, "MANIFEST id %s is not the folder's name %s", q(m.ID), id.Name)
	}
	if m.Counter != id.Counter {
		return failf(CodeManifestMismatch, "MANIFEST counter %d is not the counter %d of %s", m.Counter, id.Counter, id.Name)
	}
	if !commitPattern.MatchString(m.Commit) || !strings.HasPrefix(m.Commit, id.Commit) {
		return failf(CodeManifestMismatch, "MANIFEST commit %s does not start with %s", q(m.Commit), id.Commit)
	}
	return nil
}

var sumLine = regexp.MustCompile(`^([0-9a-f]{64}) [ *](.+)$`)

// ParseSums reads a SHA256SUMS file in the format of sha256sum: a lowercase
// hex digest, a space, a space or an asterisk, and a path relative to the
// release folder, written without ./ and without . or .. elements. It refuses
// the escaped form sha256sum writes for names holding a backslash or a
// newline, a path listed twice, and the sums file or its signature listed.
func ParseSums(data []byte) (map[string]string, error) {
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxSmallFile)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `\`) {
			return nil, failf(CodeSumsMalformed, "SHA256SUMS line %d: escaped names are not accepted", n)
		}
		m := sumLine.FindStringSubmatch(line)
		if m == nil {
			return nil, failf(CodeSumsMalformed, "SHA256SUMS line %d: not <sha256>  <path>", n)
		}
		path := m[2]
		if !fs.ValidPath(path) || path == "." || strings.Contains(path, `\`) {
			return nil, failf(CodeSumsMalformed, "SHA256SUMS line %d: path %s escapes the release folder or is not clean (relative, no ./, no . or ..)", n, q(path))
		}
		if path == SumsFile || path == SignatureFile {
			return nil, failf(CodeSumsMalformed, "SHA256SUMS line %d: %s must not be listed", n, path)
		}
		if _, dup := sums[path]; dup {
			return nil, failf(CodeSumsMalformed, "SHA256SUMS line %d: %s listed twice", n, q(path))
		}
		sums[path] = m[1]
	}
	if err := sc.Err(); err != nil {
		return nil, failf(CodeSumsMalformed, "SHA256SUMS: a line is too long")
	}
	if len(sums) == 0 {
		return nil, failf(CodeSumsMalformed, "SHA256SUMS lists no file")
	}
	return sums, nil
}

// release is an opened, verified release folder.
type release struct {
	id    ReleaseID
	dir   string // the path the release's programs are given: releases/<id>
	path  string // where the folder is now: dir, or the import's folder until it is renamed
	root  *os.Root
	b     *budget // nil: no limit (a test's look at a release)
	sums  []byte
	files int
	units []string // canonical file names, from the verified tree
	// digest is the tree's digest (see treeDigest), set by verify.
	digest string
	// Set by checkRelease.
	manifest Manifest
	lists
}

func (r *release) Close() error { return r.root.Close() }

// entryCheck is called for the release folder (as ".") and every entry in it.
type entryCheck func(rel string, info fs.FileInfo) error

// openRelease opens the folder name of parent as the release id, whose path
// for its programs is dir. The folder must be a real folder, not a symbolic
// link; every entry in it must be a folder or a regular file with one link
// (so hashing never follows a link out of it) and pass trust, when trust is
// given; and its SHA256SUMS must match every file in it.
//
// The walk and the hashing count against the budget b, when it is given.
func openRelease(id ReleaseID, parent *os.Root, name, dir string, trust entryCheck, b *budget) (*release, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failf(CodeReleaseMissing, "no release folder %s", id.Name)
		}
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, failf(CodeReleaseNotFolder, "releases/%s is a symbolic link, not a release folder", id.Name)
	}
	if !info.IsDir() {
		return nil, failf(CodeReleaseNotFolder, "releases/%s is not a folder", id.Name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if opened, err := root.Stat("."); err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, failf(CodeReleaseNotFolder, "releases/%s changed while it was opened", id.Name)
	}
	r, err := newRelease(id, root, dir, dir, trust, b)
	if err != nil {
		root.Close()
		return nil, err
	}
	return r, nil
}

// newRelease verifies the opened folder root as the release id (see
// openRelease); dir is the path its programs are given, path where it is.
// It does not close root.
func newRelease(id ReleaseID, root *os.Root, dir, path string, trust entryCheck, b *budget) (*release, error) {
	r := &release{id: id, dir: dir, path: path, root: root, b: b}
	if trust != nil {
		info, err := root.Stat(".")
		if err != nil {
			return nil, err
		}
		if err := trust(".", info); err != nil {
			return nil, err
		}
	}
	if err := r.verify(trust); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *release) verify(trust entryCheck) error {
	present := map[string]bool{}
	entries := map[string]treeEntry{}
	if info, err := r.root.Stat("."); err == nil {
		entries["."] = treeEntry{kind: "d", ino: inode(info)}
	} else {
		return err
	}
	t := tally{b: r.b}
	err := walkTree(r.root, func(rel string, info fs.FileInfo) error {
		if err := entryKind(rel, info); err != nil {
			return err
		}
		size := int64(0)
		if info.Mode().IsRegular() {
			size = info.Size()
		}
		if err := t.entry(rel, size); err != nil {
			return err
		}
		if trust != nil {
			if err := trust(rel, info); err != nil {
				return err
			}
		}
		if info.Mode().IsRegular() && rel != SumsFile && rel != SignatureFile {
			present[rel] = true
		}
		if info.IsDir() {
			entries[rel] = treeEntry{kind: "d", ino: inode(info)}
		} else {
			entries[rel] = treeEntry{kind: "f", ino: inode(info), size: info.Size()}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sums, err := r.readSmall(SumsFile)
	if err != nil {
		return err
	}
	listed, err := ParseSums(sums)
	if err != nil {
		return err
	}
	var extra []string
	for path := range present {
		if _, ok := listed[path]; !ok {
			extra = append(extra, path)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		more := ""
		if len(extra) > 1 {
			more = fmt.Sprintf(" and %d more", len(extra)-1)
		}
		return failf(CodeFileNotListed, "not listed in SHA256SUMS: %s%s", q(extra[0]), more)
	}
	paths := make([]string, 0, len(listed))
	for path := range listed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if !present[path] {
			return failf(CodeFileMissing, "listed in SHA256SUMS but missing: %s", q(path))
		}
		got, err := r.hash(path)
		if err != nil {
			return err
		}
		if got != listed[path] {
			return failf(CodeSumsMismatch, "SHA256 of %s does not match SHA256SUMS", q(path))
		}
		e := entries[path]
		e.sum = got
		entries[path] = e
	}
	if e, ok := entries[SumsFile]; ok {
		e.sum = sumOf(sums)
		entries[SumsFile] = e
	}
	if e, ok := entries[SignatureFile]; ok {
		sum, err := r.hashUnlisted(SignatureFile)
		if err != nil {
			return err
		}
		e.sum = sum
		entries[SignatureFile] = e
	}
	r.units = []string{}
	for _, p := range paths {
		if strings.HasPrefix(p, "etc/systemd/") && strings.HasSuffix(p, ".service") {
			name := filepath.Base(p)
			if !serviceName.MatchString(name) || len(name) > 255 {
				return failf(CodeBadEntry, "invalid service unit name %s", q(name))
			}
			r.units = append(r.units, name)
		}
	}
	slices.Sort(r.units)
	r.units = slices.Compact(r.units)
	r.sums = sums
	r.files = len(paths)
	r.digest = treeDigest(entries)
	return nil
}

// hash returns the SHA-256 of the file path, read to the size fstat gave
// when it was opened; a file that changes while it is read is an error.
func (r *release) hash(path string) (string, error) {
	f, info, err := openFileIn(r.root, path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if r.b != nil && info.Size() > r.b.lim.FileBytes {
		return "", failf(CodeFileTooLarge, "%s is larger than %d bytes", q(path), r.b.lim.FileBytes)
	}
	h := sha256.New()
	var in io.Reader = io.LimitReader(f, info.Size()+1)
	if r.b != nil {
		in = budgetReader{in, r.b}
	}
	n, err := io.Copy(h, in)
	if err != nil {
		if code, _ := codeOf(err); code == CodeTimeBudget {
			return "", err
		}
		return "", failf(CodeReadFailed, "%s: %s", q(path), q(err.Error()))
	}
	if after, err := f.Stat(); err != nil || n != info.Size() || !sameContent(info, after) {
		return "", failf(CodeFileChanged, "%s changed while it was read", q(path))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sameContent reports whether two fstat results of one open file say that
// it is still the regular file of the same size and time.
func sameContent(before, after fs.FileInfo) bool {
	return after.Mode().IsRegular() && after.Size() == before.Size() && after.ModTime().Equal(before.ModTime())
}

func (r *release) readSmall(path string) ([]byte, error) {
	data, err := readFileIn(r.root, path, maxSmallFile)
	if errors.Is(err, fs.ErrNotExist) {
		if path == SumsFile {
			return nil, failf(CodeSumsMissing, "%s missing in the release", path)
		}
		return nil, failf(CodeFileMissing, "%s missing in the release", q(path))
	}
	return data, err
}

// exists reports whether path is a regular file of the release; anything else
// under that name is an error.
func (r *release) exists(path string) (bool, error) {
	info, err := r.root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, failf(CodeBadEntry, "%s is not a regular file", q(path))
	}
	return true, nil
}
