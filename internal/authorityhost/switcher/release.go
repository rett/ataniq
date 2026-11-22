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
	"regexp"
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
}

var manifestKeys = []string{"id", "counter", "commit", "built_on", "go_version", "package_snapshot"}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

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
	return Manifest{
		ID:              values["id"],
		Counter:         n,
		Commit:          values["commit"],
		BuiltOn:         values["built_on"],
		GoVersion:       values["go_version"],
		PackageSnapshot: values["package_snapshot"],
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
	id       ReleaseID
	dir      string
	root     *os.Root
	sums     []byte
	files    int
	manifest Manifest
}

func (r *release) Close() error { return r.root.Close() }

// openRelease opens the release folder dir, which must be a real directory
// (not a symbolic link), and checks its SHA256SUMS against every file in it.
// Every entry must be a directory or a regular file: a symbolic link anywhere
// in the folder is refused, so hashing never follows one out of it.
func openRelease(id ReleaseID, dir string) (*release, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failf(CodeReleaseMissing, "no release folder %s", q(id.Name))
		}
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, failf(CodeReleaseNotFolder, "releases/%s is a symbolic link, not a release folder", id.Name)
	}
	if !info.IsDir() {
		return nil, failf(CodeReleaseNotFolder, "releases/%s is not a folder", id.Name)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	r := &release{id: id, dir: dir, root: root}
	if err := r.verify(); err != nil {
		root.Close()
		return nil, err
	}
	return r, nil
}

func (r *release) verify() error {
	sums, err := r.readSmall(SumsFile)
	if err != nil {
		return err
	}
	listed, err := ParseSums(sums)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	err = fs.WalkDir(r.root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		switch t := d.Type(); {
		case t&fs.ModeSymlink != 0:
			return failf(CodeBadEntry, "%s is a symbolic link; a release holds only folders and regular files", q(path))
		case t.IsDir():
			return nil
		case !t.IsRegular():
			return failf(CodeBadEntry, "%s is not a regular file", q(path))
		}
		if path != SumsFile && path != SignatureFile {
			present[path] = true
		}
		return nil
	})
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
	}
	r.sums = sums
	r.files = len(paths)
	return nil
}

// openRegular opens a regular file of the release and checks that what was
// opened is the entry that was looked at, not something put there since.
func (r *release) openRegular(path string) (*os.File, error) {
	before, err := r.root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, failf(CodeBadEntry, "%s is not a regular file", q(path))
	}
	f, err := r.root.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, failf(CodeReadFailed, "%s changed while it was read", q(path))
	}
	return f, nil
}

func (r *release) hash(path string) (string, error) {
	f, err := r.openRegular(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", failf(CodeReadFailed, "%s: %s", q(path), q(err.Error()))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *release) readSmall(path string) ([]byte, error) {
	f, err := r.openRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if path == SumsFile {
				return nil, failf(CodeSumsMissing, "%s missing in the release", path)
			}
			return nil, failf(CodeFileMissing, "%s missing in the release", q(path))
		}
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSmallFile+1))
	if err != nil {
		return nil, failf(CodeReadFailed, "%s: %s", q(path), q(err.Error()))
	}
	if len(data) > maxSmallFile {
		return nil, failf(CodeReadFailed, "%s is larger than %d bytes", q(path), maxSmallFile)
	}
	return data, nil
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
