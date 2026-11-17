package mirror

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/safefs"
)

const (
	// stagingInfix comes between the mirror's folder name and the random part
	// of a staging folder's name.
	stagingInfix = ".staging-"
	// stagingRandomBytes is the number of random bytes in the name; the name
	// holds twice as many hexadecimal digits.
	stagingRandomBytes = 8
	// stagingMarker is the file that this code writes first into every
	// staging folder. A folder without it is not removed as a leftover.
	stagingMarker = "ataniq-staging"
	// stagingMarkerText is what the marker holds.
	stagingMarkerText = "ataniq-mirror-staging 1\n"
	maxStagingMarker  = 256
	maxAlternates     = 4 << 10
)

// staging is the staging repository of one fetch: a bare repository beside
// the mirror that has the mirror's object store as its only alternate. The
// fetch from the lead's clone goes into it, and nothing reaches the mirror
// before the checks of [staging.check] have passed.
type staging struct {
	m    *Mirror
	path string
	// alternates is the content of objects/info/alternates as this code
	// wrote it.
	alternates string
}

// newStaging makes a staging repository: a folder of mode 0700 named
// <mirror path>.staging-<random>, the marker file, a bare repository from
// "git init" with no template and an empty "hooks" folder, and the alternates
// file. On failure nothing is left.
func (m *Mirror) newStaging(ctx context.Context) (*staging, error) {
	if strings.ContainsAny(m.path, "\n\x00") {
		return nil, errors.New("mirror: the mirror path holds a newline, which an alternates file cannot carry")
	}
	var b [stagingRandomBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	s := &staging{
		m:          m,
		path:       m.path + stagingInfix + hex.EncodeToString(b[:]),
		alternates: filepath.Join(m.path, "objects") + "\n",
	}
	if err := os.Mkdir(s.path, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	if err := s.populate(ctx); err != nil {
		s.remove()
		return nil, err
	}
	return s, nil
}

func (s *staging) populate(ctx context.Context) error {
	// The marker comes first, so that a crash after this point leaves a
	// folder that the next fetch recognises as its own.
	if err := os.Chmod(s.path, 0o700); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	if err := writeNew(filepath.Join(s.path, stagingMarker), stagingMarkerText); err != nil {
		return err
	}
	if _, err := s.m.run(ctx, gitenv.Cmd{
		Dir:  s.path,
		Args: []string{"init", "--bare", "--quiet", "--template="},
	}, nil, smallOutput); err != nil {
		return err
	}
	hooks := filepath.Join(s.path, "hooks")
	if err := os.RemoveAll(hooks); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	if err := os.Mkdir(hooks, 0o700); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	info := filepath.Join(s.path, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	return writeNew(filepath.Join(info, "alternates"), s.alternates)
}

// writeNew creates the file at path with mode 0600, refuses to follow a
// symlink or to replace a file, and writes text.
func writeNew(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("mirror: %w", werr)
	}
	return nil
}

// remove deletes the staging folder and everything in it. os.RemoveAll
// follows no symlink. It is safe to call twice.
func (s *staging) remove() {
	_ = os.RemoveAll(s.path)
}

// check looks at the staging repository after the fetch and returns the
// reason for a refusal, or "" when it is still what [Mirror.newStaging] made.
// The reasons are fixed words. The fetch cannot write most of this, so the
// check guards against a bug in git or in this package, and costs little:
//   - the alternates file is byte for byte what this code wrote, and there is
//     no http-alternates file;
//   - there is no shallow file, and none of the [redirects] files;
//   - the config holds none of the settings that [Open] refuses, and the
//     repository is still bare;
//   - the "hooks" folder is empty or gone.
func (s *staging) check(ctx context.Context) (string, error) {
	root, err := safefs.OpenRoot(s.path)
	if err != nil {
		return "", fmt.Errorf("mirror: the staging repository: %w", err)
	}
	defer root.Close()
	alt, err := root.ReadFile("objects/info/alternates", maxAlternates)
	if err != nil || string(alt) != s.alternates {
		return "the staging repository's alternates file is not the one written", nil
	}
	for _, rel := range []string{"objects/info/http-alternates", "shallow"} {
		if _, err := root.Lstat(rel); err == nil {
			return "the staging repository has a " + filepath.Base(rel) + " file", nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "the staging repository's " + filepath.Base(rel) + " file cannot be checked", nil
		}
	}
	if reason := checkRedirects(root); reason != "" {
		return "the staging repository: " + reason, nil
	}
	timeout, err := s.m.runLimit(ctx)
	if err != nil {
		return "", err
	}
	if reason, err := checkConfigFile(ctx, s.m.git, root, timeout); err != nil {
		return "", err
	} else if reason != "" {
		return "the staging repository: " + reason, nil
	}
	if _, err := root.Lstat("hooks"); err == nil {
		size, err := root.TreeSize("hooks", safefs.SizeLimits{MaxBytes: 1 << 20, MaxEntries: 8})
		if err != nil || size.Entries != 1 {
			return "the staging repository's hooks folder is not empty", nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "the staging repository's hooks folder cannot be checked", nil
	}
	return "", nil
}

// stagingName reports whether name is the name of a staging folder of the
// mirror whose folder name is base: base, the infix and the random part, which
// is lower-case hexadecimal.
func stagingName(base, name string) bool {
	rest, ok := strings.CutPrefix(name, base+stagingInfix)
	if !ok || len(rest) != 2*stagingRandomBytes {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// removeStale deletes the staging folders that a crashed fetch left beside the
// mirror. The caller holds the fetch lock, so no fetch of this mirror is
// running. A folder is removed only when its name has the form of
// [stagingName] and it is a real folder of the calling user that holds the
// marker file with the text that this code writes. A folder without the
// marker, a symlink and anything else is left alone. The removal follows no
// symlink. Failures are not reported: a leftover costs space and nothing
// else, and the next fetch tries again.
func (m *Mirror) removeStale() {
	parent, base := filepath.Dir(m.path), filepath.Base(m.path)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !stagingName(base, e.Name()) {
			continue
		}
		path := filepath.Join(parent, e.Name())
		if !isOurStaging(path) {
			continue
		}
		_ = os.RemoveAll(path)
	}
}

// isOurStaging reports whether path is a real folder of the calling user that
// holds the staging marker. A symlink at path is refused by safefs.
func isOurStaging(path string) bool {
	root, err := safefs.OpenRoot(path)
	if err != nil {
		return false
	}
	defer root.Close()
	fi, err := root.Lstat(".")
	if err != nil {
		return false
	}
	if uid, ok := ownerUID(fi); !ok || int(uid) != os.Geteuid() {
		return false
	}
	data, err := root.ReadFile(stagingMarker, maxStagingMarker)
	return err == nil && string(data) == stagingMarkerText
}
