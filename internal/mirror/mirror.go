package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/safefs"
)

// ErrNotMirror is returned by [Open] for a folder that is not an exchange
// mirror as [Create] makes it. The error text says why, in words fixed in this
// package.
var ErrNotMirror = errors.New("mirror: not an exchange mirror")

const (
	// markerName is the file that marks a folder as a mirror.
	markerName = "ataniq-mirror"
	// FormatVersion is the version written in the marker file.
	FormatVersion = 1

	maxMarker = 4 << 10
	maxConfig = 64 << 10
)

// marker is the content of the marker file.
type marker struct {
	Format  int    `json:"format"`
	Project string `json:"project"`
}

// settings are the repository settings that [Create] writes with "git config",
// and why. The owner's choices (receive.denyDeletes, receive.denyNonFastForwards)
// are not set: the owner is the only writer of upstream refs and decides.
var settings = [][2]string{
	// A bare repository keeps no reflog by default. With "always" every
	// ref update is recorded, so a lead branch that moved can be traced.
	{"core.logAllRefUpdates", "always"},
	// No automatic garbage collection: it could drop objects that a review
	// still names. The owner decides when to pack or prune.
	{"gc.auto", "0"},
	// Check every object that arrives, for a fetch and for a push.
	{"transfer.fsckObjects", "true"},
	// Make the checks for look-alikes of ".git" the same on every platform.
	{"core.protectNTFS", "true"},
	{"core.protectHFS", "true"},
}

// Mirror is an exchange mirror that [Create] made or [Open] checked. The
// check is a snapshot: the methods do not repeat it. A Mirror is safe for
// concurrent use.
type Mirror struct {
	git     gitenv.Git
	path    string
	project string
	limits  Limits
}

// Path returns the absolute path of the mirror's folder.
func (m *Mirror) Path() string { return m.path }

// Git returns the git executable that the mirror runs.
func (m *Mirror) Git() gitenv.Git { return m.git }

// WithLimits returns a copy of the mirror with the given limits. A zero field
// keeps the default; a negative one is an error.
func (m *Mirror) WithLimits(l Limits) (*Mirror, error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	c := *m
	c.limits = l
	return &c, nil
}

// Limits returns the limits of the mirror, with the defaults filled in. Only
// Timeout and MaxRefs are set: they are the two fields that a mirror reads.
func (m *Mirror) Limits() Limits {
	return Limits{Timeout: m.limits.timeout(), MaxRefs: m.limits.maxRefs()}
}

func notMirror(reason string) error { return fmt.Errorf("%w: %s", ErrNotMirror, reason) }

// projectOf checks the path of a mirror and returns the project name that its
// last element holds.
func projectOf(path string) (string, error) {
	if err := checkAbsClean("mirror path", path); err != nil {
		return "", err
	}
	project, ok := strings.CutSuffix(filepath.Base(path), ".git")
	if !ok || !ValidName(project) {
		return "", errors.New("mirror: the last element of the path must be <project>.git with a valid project name")
	}
	return project, nil
}

// Create makes a new mirror at path, which must be <state>/mirrors/<project>.git
// (see [Path]) and must not exist. The parent must exist, be a real folder
// (not a symlink) and belong to the calling user.
//
// The folder gets mode 0700. Create runs "git init --bare" with no template,
// so the sample hooks are not copied, and leaves "hooks" as an empty folder.
// HEAD is left as git makes it. It writes the repository settings listed in
// the source and the marker file "ataniq-mirror", which holds the format
// version and the project name taken from the folder name. When anything
// fails, the folder is removed again. On success the result is the same as
// [Open] returns. The options can set the limits of the git runs: see
// [UseLimits].
func Create(ctx context.Context, git gitenv.Git, path string, opts ...Option) (*Mirror, error) {
	project, err := projectOf(path)
	if err != nil {
		return nil, err
	}
	lim, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	pi, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("mirror: parent folder: %w", err)
	}
	if !pi.IsDir() {
		return nil, errors.New("mirror: the parent is not a real folder")
	}
	uid, ok := ownerUID(pi)
	if !ok {
		return nil, safefs.ErrUnsupported
	}
	if int(uid) != os.Geteuid() {
		return nil, errors.New("mirror: the parent folder does not belong to the calling user")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	m, err := populate(ctx, git, path, project, lim)
	if err != nil {
		// The folder is new and ours, and the parent is the owner's.
		_ = os.RemoveAll(path)
		return nil, err
	}
	return m, nil
}

// populate fills the new, empty folder and returns the checked mirror.
func populate(ctx context.Context, git gitenv.Git, path, project string, lim Limits) (*Mirror, error) {
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	if _, err := runGit(ctx, git, gitenv.Cmd{
		Dir:  path,
		Args: []string{"init", "--bare", "--quiet", "--template="},
	}, nil, lim.timeout(), smallOutput); err != nil {
		return nil, err
	}
	hooks := filepath.Join(path, "hooks")
	if err := os.RemoveAll(hooks); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	if err := os.Mkdir(hooks, 0o700); err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	for _, kv := range settings {
		if _, err := runGit(ctx, git, gitenv.Cmd{
			GitDir: path,
			Args:   []string{"config", kv[0], kv[1]},
		}, nil, lim.timeout(), smallOutput); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(marker{Format: FormatVersion, Project: project})
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(path, markerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	_, werr := f.Write(append(data, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("mirror: %w", werr)
	}
	return Open(ctx, git, path, UseLimits(lim))
}

// Open checks the mirror at path and returns it. It refuses, with an error
// that matches [ErrNotMirror] and says why:
//   - a path that is not absolute and clean, or whose last element is not
//     <project>.git with a valid project name;
//   - a path that is a symlink, or not a folder, or missing;
//   - a folder that does not belong to the calling user, or that group or
//     others can write;
//   - a missing or malformed marker file, or one for another project;
//   - a repository that is not bare, or that lacks HEAD, objects or refs;
//   - a "hooks" folder that is not empty;
//   - an objects/info/alternates or objects/info/http-alternates file, or a
//     commondir, gitdir or config.worktree file in the folder (each sends git
//     to another repository or to more configuration);
//   - a config file with an include or includeIf, an extensions section (any
//     key), or a setting of
//     core.hooksPath, core.fsmonitor, core.sshCommand,
//     core.alternateRefsCommand, core.worktree, uploadpack.packObjectsHook,
//     diff.external, or diff.<driver>.command or textconv, or any url.*,
//     filter.*, remote.* or alias.* entry. Other sections are not looked at.
//
// The mirror is Ataniq's own, so this is a consistency check that catches a
// mirror somebody edited by hand. All file checks use safefs and follow no
// symlink. The config is read once through safefs and parsed by git from that
// copy; git does not read the repository before the config is accepted. The
// options can set the limits of the mirror and of the git runs of Open: see
// [UseLimits].
func Open(ctx context.Context, git gitenv.Git, path string, opts ...Option) (*Mirror, error) {
	project, err := projectOf(path)
	if err != nil {
		// A path that cannot name a mirror is not a mirror.
		return nil, fmt.Errorf("%w: %w", ErrNotMirror, err)
	}
	lim, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	root, err := safefs.OpenRoot(path)
	if err != nil {
		switch {
		case errors.Is(err, safefs.ErrUnsupported):
			return nil, err
		case errors.Is(err, safefs.ErrEscape):
			return nil, notMirror("the path is a symlink")
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("%w: the path does not exist: %w", ErrNotMirror, fs.ErrNotExist)
		}
		return nil, notMirror("the path is not a folder that can be opened")
	}
	defer root.Close()
	if reason, err := checkFolder(root, project); err != nil {
		return nil, err
	} else if reason != "" {
		return nil, notMirror(reason)
	}
	if reason, err := checkConfigFile(ctx, git, root, lim.timeout()); err != nil {
		return nil, err
	} else if reason != "" {
		return nil, notMirror(reason)
	}
	out, err := runGit(ctx, git, gitenv.Cmd{
		GitDir: path,
		Args:   []string{"rev-parse", "--is-bare-repository"},
	}, nil, lim.timeout(), smallOutput)
	if err != nil {
		var ge *gitExit
		if errors.As(err, &ge) {
			return nil, notMirror("git does not read the folder as a repository")
		}
		return nil, err
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, notMirror("the repository is not bare")
	}
	return &Mirror{git: git, path: path, project: project, limits: lim}, nil
}

// checkConfigFile reads the file "config" of the repository folder that root
// names, once, through safefs, and has git parse that copy. It returns the
// reason for a refusal ("" when the config is fine), or an error for a failure
// that is not a refusal. The listing needs no repository: --git-dir names a
// path that is not one, so that git does not read the folder's own config.
func checkConfigFile(ctx context.Context, git gitenv.Git, root *safefs.Root, timeout time.Duration) (string, error) {
	config, err := root.ReadFile("config", maxConfig)
	if err != nil {
		return "the config file cannot be read", nil
	}
	listing, err := runGit(ctx, git, gitenv.Cmd{
		GitDir: "/dev/null",
		Args:   []string{"config", "--file=/dev/stdin", "--list", "-z"},
	}, bytes.NewReader(config), timeout, maxConfig*2)
	if err != nil {
		var ge *gitExit
		if errors.As(err, &ge) {
			return "the config file is malformed", nil
		}
		return "", err
	}
	return checkConfig(listing), nil
}

// checkFolder runs the checks that need only the folder handle. It returns a
// reason for a refusal, or an error for a failure that is not a refusal.
func checkFolder(root *safefs.Root, project string) (string, error) {
	fi, err := root.Lstat(".")
	if err != nil {
		return "", fmt.Errorf("mirror: %w", err)
	}
	uid, ok := ownerUID(fi)
	if !ok {
		return "", safefs.ErrUnsupported
	}
	if int(uid) != os.Geteuid() {
		return "the folder does not belong to the calling user", nil
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "the folder is writable by group or others", nil
	}

	data, err := root.ReadFile(markerName, maxMarker)
	if err != nil {
		return "the marker file is missing or unreadable", nil
	}
	var mk marker
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&mk); err != nil || dec.More() {
		return "the marker file is malformed", nil
	}
	if mk.Format != FormatVersion {
		return "the marker file has an unknown format version", nil
	}
	if mk.Project != project {
		return "the marker file names another project", nil
	}

	for _, d := range []string{"objects", "refs", "hooks"} {
		isDir, err := root.IsDir(d)
		if err != nil || !isDir {
			return "the folder " + d + " is missing or is not a real folder", nil
		}
	}
	if hi, err := root.Lstat("HEAD"); err != nil || !hi.Mode().IsRegular() {
		return "HEAD is missing or is not a regular file", nil
	}
	// A folder that holds nothing counts one entry: itself.
	size, err := root.TreeSize("hooks", safefs.SizeLimits{MaxBytes: 1 << 20, MaxEntries: 8})
	if err != nil {
		if errors.Is(err, safefs.ErrLimit) {
			return "the hooks folder is not empty", nil
		}
		return "the hooks folder cannot be read", nil
	}
	if size.Entries != 1 {
		return "the hooks folder is not empty", nil
	}
	for _, alt := range []string{"objects/info/alternates", "objects/info/http-alternates"} {
		_, err := root.Lstat(alt)
		if err == nil {
			return "the repository has an alternates file", nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "the alternates file cannot be checked", nil
		}
	}
	return checkRedirects(root), nil
}

// redirects are the files in a repository folder that send git to another
// repository or make it read more configuration: commondir and gitdir name
// another repository, and config.worktree is read when the extension
// worktreeConfig is set. A repository that Ataniq made has none of them.
var redirects = []string{"commondir", "gitdir", "config.worktree"}

// checkRedirects returns the reason for a refusal when the repository folder
// that root names has one of the [redirects] files, in any form, or when one
// cannot be checked. It returns "" otherwise.
func checkRedirects(root *safefs.Root) string {
	for _, name := range redirects {
		_, err := root.Lstat(name)
		if err == nil {
			return "the repository has a " + name + " file"
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "the " + name + " file cannot be checked"
		}
	}
	return ""
}

// Size counts the allocated bytes and the entries of the whole mirror
// folder. The walk follows no symlink: a symlink counts as one entry and is
// never opened (see safefs). It stops with [safefs.ErrLimit] when a limit is
// passed.
func (m *Mirror) Size(ctx context.Context, lim safefs.SizeLimits) (safefs.Size, error) {
	if err := ctx.Err(); err != nil {
		return safefs.Size{}, err
	}
	root, err := safefs.OpenRoot(m.path)
	if err != nil {
		return safefs.Size{}, err
	}
	defer root.Close()
	return root.TreeSize(".", lim)
}
