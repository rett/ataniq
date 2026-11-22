package switcher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
)

// ImportedDir holds, in the state folder, one provenance record per
// release the import made: var/lib/authority-host/imported/<id>.
const ImportedDir = "imported"

// maxProvenance bounds a provenance record.
const maxProvenance = 4 << 10

// provenance is the record the import writes for the release it made: the
// digest of the tree as it made it.
type provenance struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Files  int    `json:"files"`
	Time   string `json:"time"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// treeEntry is what the digest of a release tree covers of one entry.
type treeEntry struct {
	kind string // d or f
	ino  uint64
	size int64
	sum  string // the SHA-256 of a file
}

// treeDigest is the digest of a release tree: every entry's path, kind,
// inode number and size, and every file's SHA-256, in the order of the
// paths. The inode numbers bind it to the files the import made: a tree
// with the same content made otherwise (copied by hand, restored from a
// backup) has other inodes and another digest. The modes are not in it
// (the import seals the tree after it is verified); the trust check of a
// release covers them.
func treeDigest(entries map[string]treeEntry) string {
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		e := entries[p]
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%s\n", p, e.kind, e.ino, e.size, e.sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func inode(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

func sumOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// hashUnlisted returns the SHA-256 of a file that is in the tree but not listed
// in its sums (the sums themselves are read whole elsewhere; the
// signature).
func (r *release) hashUnlisted(p string) (string, error) {
	f, info, err := openFileIn(r.root, p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxSmallFile+1))
	if err != nil {
		return "", failf(CodeReadFailed, "%s: %s", q(p), q(err.Error()))
	}
	if n > maxSmallFile || n != info.Size() {
		return "", failf(CodeFileChanged, "%s changed while it was read, or is larger than %d bytes", q(p), maxSmallFile)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// provenanceError is the refusal of a release folder the import did not
// make, or that changed since.
func provenanceError(code, id, why string) error {
	return failf(code, "releases/%s %s; only a release made by ah-switch --from is switched to: move the folder out of releases/ and import it again with --from, which makes a fresh copy", id, why)
}

// openImported opens the folder of provenance records, made when create.
func (s *Switcher) openImported(create bool) (*os.Root, error) {
	return s.openTrusted(path.Join(StateDir, ImportedDir), create)
}

// writeProvenance records that the import made rel, as it is now.
func (s *Switcher) writeProvenance(rel *release) error {
	dir, err := s.openImported(true)
	if err != nil {
		return err
	}
	defer dir.Close()
	return writeJSONIn(dir, rel.id.Name, provenance{ID: rel.id.Name, Digest: rel.digest, Files: rel.files, Time: s.now()}, 0o644)
}

// checkProvenance accepts a release folder already in releases/ only when
// the import made it: its provenance record is there, trusted as a state
// file is, and names the digest the tree has now.
func (s *Switcher) checkProvenance(rel *release) error {
	dir, err := s.openImported(false)
	if errors.Is(err, fs.ErrNotExist) {
		return provenanceError(CodeProvenanceMissing, rel.id.Name, "has no import record")
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	full := filepath.Join(s.stateDir(), ImportedDir, rel.id.Name)
	f, info, err := openFileIn(dir, rel.id.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return provenanceError(CodeProvenanceMissing, rel.id.Name, "has no import record")
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err := s.trusted(full, info, false); err != nil {
		return err
	}
	if links(info) != 1 || info.Size() > maxProvenance {
		return failf(CodeUntrusted, "%s has more than one link or is too large", q(full))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProvenance+1))
	if err != nil || len(data) > maxProvenance {
		return failf(CodeReadFailed, "%s cannot be read", q(full))
	}
	var p provenance
	if err := decodeStrict(data, &p); err != nil || p.ID != rel.id.Name || !digestPattern.MatchString(p.Digest) {
		return failf(CodeStateInvalid, "%s is not an import record of %s", q(full), rel.id.Name)
	}
	if p.Digest != rel.digest {
		return provenanceError(CodeProvenanceMismatch, rel.id.Name, "is not the tree the import made (its files or their inodes changed)")
	}
	return nil
}
