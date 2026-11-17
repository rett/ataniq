package plan

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// Bounds on what a plan holds.
const (
	// MaxArgvBytes is the most bytes the whole command line may take, every
	// argument counted with its terminating NUL. Linux accepts at least this
	// much on any system; a plan above it is refused rather than left to fail
	// at exec.
	MaxArgvBytes = 128 << 10
	// MinSeccompFD is the lowest descriptor number the seccomp file may use:
	// 0, 1 and 2 are the standard streams.
	MinSeccompFD = 3
	// MaxSeccompFD is the highest descriptor number the seccomp file may use.
	MaxSeccompFD = 1023
)

// The notes every plan carries. Verify checks that they are present.
const (
	// NoteShareNet explains --share-net.
	NoteShareNet = "--share-net is passed because bubblewrap runs inside the network namespace that pasta made; it does not share the host's network"
	// NoteNestedUserns states owner decision R17.
	NoteNestedUserns = "nested user namespaces stay available (owner decision R17): --disable-userns and --assert-userns-disabled are not passed, so code inside can create user namespaces and reach kernel code that is reachable only through them; this is a larger kernel surface than with them disabled"
	// NoteNoSeccomp is the note of a plan without a seccomp file.
	NoteNoSeccomp = "no seccomp filter: none has been reviewed"
	// NoteSession explains why bubblewrap does not make the session.
	NoteSession = "bubblewrap does not make the session (--new-session is never passed): the probe gives the agent its session and controlling terminal, so the launcher does not set a controlling terminal"
)

// Refusal is the error Build returns for a problem with the options or with
// the plan it would make. A problem with the profile inputs is the profile's
// own *profile.Refusal, returned unchanged.
type Refusal struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *Refusal) Error() string { return "plan: " + e.Field + ": " + e.Reason }

func refuse(field, format string, args ...any) error {
	return &Refusal{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// Options is what the caller adds to the profile inputs.
type Options struct {
	// PastaPath and BwrapPath are the absolute paths of the pasta and
	// bubblewrap executables, as the caller found and checked them. The plan
	// never looks a program up in PATH.
	PastaPath string
	BwrapPath string

	// AgentArgv is the agent's command line as seen inside: the first element
	// is an absolute path, no element holds a NUL.
	AgentArgv []string

	// WorkDir is the working folder inside. Empty means the lead's tree. It
	// must be the tree, the lead's home, or a path below either.
	WorkDir string

	// HostPresent says which optional host rows exist: the key is the row's
	// inside path ("/etc/ssl", "/lib64"), the value true when the host has it
	// (for a symlink row, a path of the same name). A row that is optional and
	// not present is left out of the plan and noted. A nil map means none is
	// present.
	HostPresent map[string]bool

	// SeccompFD is the descriptor number the launcher will use for the
	// seccomp file, from MinSeccompFD to MaxSeccompFD. It is required when
	// the inputs name a seccomp file and must be zero when they do not.
	SeccompFD int
}

// File is a descriptor the launcher must open and pass to the first process of
// the command, at the number FD.
type File struct {
	FD int
	// Path is the host path the launcher opens, read-only.
	Path string
	// SHA256 is the digest the caller gave for the file. This package does
	// not read the file; the launcher opens it and may compare the digest.
	SHA256 string
	// Purpose says what the descriptor is for.
	Purpose string
}

// Plan is a launch plan. Argv is Pasta, Bwrap and Inner joined.
type Plan struct {
	// Argv is the whole command: pasta, its arguments, "--", bwrap, its
	// arguments, "--", the probe command line.
	Argv []string
	// Pasta is the pasta executable and its arguments, ending in "--".
	Pasta []string
	// Bwrap is the bubblewrap executable and its arguments, ending in "--".
	Bwrap []string
	// Inner is the probe command line: /ataniq/probe --spec FILE -- AGENT...
	Inner []string

	// Env is the environment allowlist with values, sorted by name. The
	// bubblewrap arguments set exactly these, after --clearenv.
	Env []profile.EnvVar
	// Files are the descriptors the launcher must open and pass: the seccomp
	// file at its number, or none.
	Files []File
	// Digest is the profile digest (profile.Inputs.Digest).
	Digest string
	// Notes say what the plan does not show by itself: the R17 surface, the
	// seccomp state, the network arrangement and each optional row left out.
	Notes []string
	// WorkDir is the working folder inside, as --chdir gives it.
	WorkDir string

	// SetControllingTerminal says whether the launcher makes the terminal the
	// controlling terminal of the first process. It is always false: the probe
	// makes the session and takes the terminal (variant B of the spike), and
	// bubblewrap's --new-session is never passed. A launcher that set it would
	// put the terminal under pasta's session instead.
	SetControllingTerminal bool
}

// Build validates the inputs and the options and returns the launch plan.
// Nothing is started, opened or read. The error is the profile's
// *profile.Refusal for a bad input and a *Refusal for a bad option or a plan
// that cannot be made.
func Build(in profile.Inputs, opt Options) (*Plan, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	workDir, err := opt.check(in)
	if err != nil {
		return nil, err
	}
	flags, err := in.Flags()
	if err != nil {
		return nil, err
	}
	env, err := in.Environment()
	if err != nil {
		return nil, err
	}
	binds, err := in.Binds()
	if err != nil {
		return nil, err
	}
	digest, err := in.Digest()
	if err != nil {
		return nil, err
	}

	e := &emitter{}
	e.raw(flags...)
	for _, v := range env {
		e.opt("--setenv", v.Name, v.Value)
	}
	var files []File
	if in.SeccompEnabled() {
		e.opt("--seccomp", strconv.Itoa(opt.SeccompFD))
		files = append(files, File{FD: opt.SeccompFD, Path: in.SeccompFile, SHA256: in.SeccompSHA256, Purpose: "seccomp"})
	}
	var left []string
	for _, b := range binds {
		if b.Kind == profile.KindRemountRO {
			continue // last, after --chdir
		}
		if b.Optional && !opt.HostPresent[b.Inside] {
			left = append(left, optionalNote(b))
			continue
		}
		e.bind(b)
	}
	e.opt("--chdir", workDir)
	e.opt("--remount-ro", "/")
	e.raw("--")
	if e.err != nil {
		return nil, e.err
	}

	p := &Plan{
		Pasta:   append([]string{opt.PastaPath}, sandboxnet.PastaArgs()...),
		Bwrap:   append([]string{opt.BwrapPath}, e.args...),
		Inner:   append([]string{profile.ProbeInside, "--spec", profile.SpecFileInside, "--"}, opt.AgentArgv...),
		Env:     env,
		Files:   files,
		Digest:  digest,
		WorkDir: workDir,
		Notes:   notes(in, opt, left),
	}
	p.Argv = join(p.Pasta, p.Bwrap, p.Inner)
	if n := argvBytes(p.Argv); n > MaxArgvBytes {
		return nil, refuse("command line", "is %d bytes, above the cap of %d", n, MaxArgvBytes)
	}
	if bad := forbiddenIn(p.Argv); bad != "" {
		return nil, refuse("command line", "holds the forbidden option %s", bad)
	}
	if probs := Verify(p, in); len(probs) > 0 {
		return nil, refuse("plan", "fails its own check (%d problems, first: %s)", len(probs), probs[0])
	}
	return p, nil
}

// check validates the options against the inputs and returns the working
// folder.
func (o Options) check(in profile.Inputs) (string, error) {
	if err := checkPath("pasta executable", o.PastaPath); err != nil {
		return "", err
	}
	if err := checkPath("bubblewrap executable", o.BwrapPath); err != nil {
		return "", err
	}
	if err := o.checkAgent(); err != nil {
		return "", err
	}
	if err := o.checkSeccomp(in); err != nil {
		return "", err
	}
	tree := profile.TreeInside(in.Project)
	workDir := o.WorkDir
	if workDir == "" {
		workDir = tree
	}
	if err := checkPath("working folder", workDir); err != nil {
		return "", err
	}
	if !within(workDir, tree) && !within(workDir, profile.HomeInside) {
		return "", refuse("working folder", "%s is outside the tree %s and the home %s", workDir, tree, profile.HomeInside)
	}
	return workDir, nil
}

func (o Options) checkAgent() error {
	if len(o.AgentArgv) == 0 {
		return refuse("agent argv", "is empty")
	}
	if err := checkPath("agent executable", o.AgentArgv[0]); err != nil {
		return err
	}
	size := 0
	for i, a := range o.AgentArgv {
		if strings.IndexByte(a, 0) >= 0 {
			return refuse("agent argv", "element %d holds a NUL", i)
		}
		for _, f := range profile.ForbiddenFlags() {
			if a == f {
				return refuse("agent argv", "element %d is the forbidden option %s", i, f)
			}
		}
		// Bounded before anything is built from it.
		if size += len(a) + 1; size > MaxArgvBytes {
			return refuse("agent argv", "is longer than the command-line cap of %d bytes", MaxArgvBytes)
		}
	}
	return nil
}

func (o Options) checkSeccomp(in profile.Inputs) error {
	switch {
	case in.SeccompEnabled() && o.SeccompFD == 0:
		return refuse("seccomp descriptor", "missing: the inputs name a seccomp file, so the launcher must give the descriptor number it will use")
	case in.SeccompEnabled() && (o.SeccompFD < MinSeccompFD || o.SeccompFD > MaxSeccompFD):
		return refuse("seccomp descriptor", "%d is outside %d to %d (0 to 2 are the standard streams)", o.SeccompFD, MinSeccompFD, MaxSeccompFD)
	case !in.SeccompEnabled() && o.SeccompFD != 0:
		return refuse("seccomp descriptor", "%d is given but the inputs name no seccomp file", o.SeccompFD)
	}
	return nil
}

// checkPath is profile.CheckPath with the refusal turned into this package's.
func checkPath(field, p string) error {
	if err := profile.CheckPath(field, p); err != nil {
		var r *profile.Refusal
		if errors.As(err, &r) {
			return refuse(r.Field, "%s", r.Reason)
		}
		return err
	}
	return nil
}

// within reports whether p is dir or lies below it (both clean and absolute).
func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// argvBytes is the size of the command line as exec counts it.
func argvBytes(argv []string) int {
	n := 0
	for _, a := range argv {
		n += len(a) + 1
	}
	return n
}

// forbiddenIn returns the first forbidden option that is an argument of argv,
// or "".
func forbiddenIn(argv []string) string {
	for _, a := range argv {
		for _, f := range profile.ForbiddenFlags() {
			if a == f {
				return f
			}
		}
	}
	return ""
}

func optionalNote(b profile.Bind) string {
	what := "read-only bind"
	if b.Kind == profile.KindSymlink {
		what = "symlink"
	}
	return fmt.Sprintf("optional %s %s left out: the caller says the host has no such path", what, b.Inside)
}

func notes(in profile.Inputs, opt Options, left []string) []string {
	n := []string{NoteShareNet, NoteNestedUserns}
	if in.SeccompEnabled() {
		n = append(n, fmt.Sprintf("seccomp file named by the caller (sha256 %s), passed to bubblewrap as descriptor %d: this package neither reads nor generates a filter, and the descriptor must stay open across pasta's start of bubblewrap, which qualification has to show", in.SeccompSHA256, opt.SeccompFD))
	} else {
		n = append(n, NoteNoSeccomp)
	}
	if in.Identity == profile.IdentityOwner {
		n = append(n, fmt.Sprintf("identity: the owner's ids %d:%d inside (the fallback mode)", in.OwnerUID, in.OwnerGID))
	} else {
		n = append(n, "identity: uid and gid 0 inside, mapped to the owner outside; tools that run as UID 0 are untested")
	}
	n = append(n, "network: pasta arguments of sandboxnet.PastaArgs (outbound NAT, no port forwarding); outbound traffic is not narrowed", NoteSession)
	return append(n, left...)
}
