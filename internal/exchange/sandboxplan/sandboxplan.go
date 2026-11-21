package sandboxplan

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNotConfigured is returned, wrapped with the name of the request, when
// a planner has no sandbox for it. A caller refuses the request and does not
// run the command without a sandbox.
var ErrNotConfigured = errors.New("no sandbox configured")

// MaxMeasureDigits is the most digits [ParseMeasure] accepts.
const MaxMeasureDigits = 18

// Command is a process to start: an argv and a whole environment.
type Command struct {
	// Argv is the command line. Argv[0] is an absolute path. No element
	// holds a NUL byte.
	Argv []string
	// Env is the whole environment of the process, as KEY=VALUE entries.
	// The caller's own environment is not added to it.
	Env []string
}

// Validate reports why the command cannot be started: an empty argv, an
// Argv[0] that is not absolute, a NUL byte in any element, or an
// environment entry that is not KEY=VALUE with a non-empty key.
func (c Command) Validate() error {
	if err := validArgv(c.Argv); err != nil {
		return err
	}
	return validEnv(c.Env)
}

func validArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty argv")
	}
	for i, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return fmt.Errorf("argv[%d] holds a NUL byte", i)
		}
	}
	if !filepath.IsAbs(argv[0]) {
		return errors.New("argv[0] is not an absolute path")
	}
	return nil
}

func validEnv(env []string) error {
	for i, e := range env {
		if strings.IndexByte(e, 0) >= 0 {
			return fmt.Errorf("env[%d] holds a NUL byte", i)
		}
		k, _, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return fmt.Errorf("env[%d] is not KEY=VALUE", i)
		}
	}
	return nil
}

func validPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case strings.ContainsAny(p, "\x00\n{}"):
		return errors.New("path holds a NUL, newline or brace")
	case !filepath.IsAbs(p):
		return errors.New("path is not absolute")
	case filepath.Clean(p) != p:
		return errors.New("path is not clean")
	}
	return nil
}

// validRoot is validPath for a root folder: the folder of the file system
// itself is not a root.
func validRoot(p string) error {
	if err := validPath(p); err != nil {
		return err
	}
	if p == "/" {
		return errors.New("path is the root of the file system")
	}
	return nil
}

// within reports whether a is b or lies below b. Both are clean.
func within(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/")
}

// ServeRequest asks for the sandboxed serving half of a git fetch.
//
// The caller vouches for Tree before it asks for a plan. Every component
// of the path, its last name included, must be one that no sandbox can
// write: no sandbox may create, replace or rename any folder on the way to
// the clone or the entry of the clone itself. The one exception is that the
// content of the clone is written by sandboxed code. A lead's clone under a
// folder the lead can write fails this, because the lead can swap the clone's
// entry for a symlink. The caller checks every component without following
// symlinks. A plan cannot do it: this package reads no file.
type ServeRequest struct {
	// Role and Project are names that pass [ValidName].
	Role, Project string
	// Tree is the absolute host path of the lead's clone. See the
	// precondition above.
	Tree string
}

// Validate reports why the request cannot be planned: a role or a project
// that fails [ValidName], or a Tree that is not an absolute, clean path
// without a NUL byte, newline or brace, or that is the root of the file
// system. It does not read any file, so it cannot check the precondition
// above.
func (r ServeRequest) Validate() error {
	if !ValidName(r.Role) {
		return errors.New("role is not a valid name")
	}
	if !ValidName(r.Project) {
		return errors.New("project is not a valid name")
	}
	if err := validRoot(r.Tree); err != nil {
		return fmt.Errorf("tree: %w", err)
	}
	return nil
}

// ServePlan is how to serve one lead's clone.
type ServePlan struct {
	// UploadPack is the complete argv that runs git-upload-pack sandboxed.
	// Git appends the repository path as one more argument. Git starts the
	// command, through sh, with the environment of the process that
	// fetches, and the plan has no environment for it. So the argv must
	// clear the environment itself, for example with bwrap --clearenv or
	// env -i.
	UploadPack []string
	// Repository is the path of the lead's clone as the serving half
	// sees it, inside the sandbox. It holds no "@[" (see Validate).
	Repository string
	// Measure prints the size of the clone's object store: the first
	// whitespace-delimited token of the first output line is a decimal
	// byte count. See [ParseMeasure]. The number is the lead side's
	// statement and a first check only.
	Measure Command
}

// Validate reports why the plan cannot be used.
func (p ServePlan) Validate() error {
	if err := validArgv(p.UploadPack); err != nil {
		return fmt.Errorf("upload pack: %w", err)
	}
	if err := validPath(p.Repository); err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	if strings.Contains(p.Repository, "@[") {
		// The fetch gives the owner's git this path as a "file://" URL, and
		// git cuts "[...]" after "@[" out of the path of such a URL, however
		// it is written, so the sandbox would be asked for another path.
		return errors.New(`repository: path holds "@[", which git changes in the URL of a fetch`)
	}
	if err := p.Measure.Validate(); err != nil {
		return fmt.Errorf("measure: %w", err)
	}
	return nil
}

// VerifyRequest asks for the sandbox that verifies a lead branch. All
// paths are absolute host paths. Scratch and Home are empty folders that the
// caller made.
//
// The caller vouches for Mirror, Scratch and Home before it asks for a plan.
// Scratch and Home must not be reused across plans. Every component of each
// path, its last name included, must be one that no sandbox can write: no
// sandbox may create, replace or rename any folder on the way to the path or
// its entry itself.
// The exceptions are that Scratch and Home are folders whose content
// sandboxed code writes. The verify sandbox must not write the mirror at
// all. The caller checks every component without following symlinks. A plan
// cannot do it: this package reads no file.
type VerifyRequest struct {
	// Project is a name that passes [ValidName].
	Project string
	// Mirror, Scratch and Home are the host paths. See the precondition
	// above.
	Mirror, Scratch, Home string
}

// Validate reports why the request cannot be planned: a project that fails
// [ValidName], a path that is not absolute and clean, holds a NUL byte,
// newline or brace, or is the root of the file system, or two of Mirror,
// Scratch and Home that are the same or one inside the other. It does not
// read any file, so it cannot check the precondition above.
func (r VerifyRequest) Validate() error {
	if !ValidName(r.Project) {
		return errors.New("project is not a valid name")
	}
	roots := []struct{ name, path string }{
		{"mirror", r.Mirror}, {"scratch", r.Scratch}, {"home", r.Home},
	}
	for _, f := range roots {
		if err := validRoot(f.path); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	for i, a := range roots {
		for _, b := range roots[i+1:] {
			if within(a.path, b.path) || within(b.path, a.path) {
				return fmt.Errorf("%s and %s are the same folder or one is inside the other", a.name, b.name)
			}
		}
	}
	return nil
}

// VerifyPlan is how to run commands in the verification sandbox.
type VerifyPlan struct {
	// Prefix is the complete sandbox argv. The caller appends the argv of
	// the inner command: see [VerifyPlan.Command].
	Prefix []string
	// Env is the environment for starting Prefix.
	Env []string
	// Mirror, Scratch and Home are the three folders of the request as the
	// inner command sees them.
	Mirror, Scratch, Home string
}

// Validate reports why the plan cannot be used.
func (p VerifyPlan) Validate() error {
	if err := validArgv(p.Prefix); err != nil {
		return fmt.Errorf("prefix: %w", err)
	}
	if err := validEnv(p.Env); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	for _, f := range []struct{ name, path string }{
		{"mirror", p.Mirror}, {"scratch", p.Scratch}, {"home", p.Home},
	} {
		if err := validPath(f.path); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

// Command returns the command that runs inner in the sandbox: Prefix
// followed by inner, with Env. The inner argv must be non-empty, start with
// an absolute path and hold no NUL byte. The result shares no memory with
// the plan.
func (p VerifyPlan) Command(inner []string) (Command, error) {
	if err := validArgv(p.Prefix); err != nil {
		return Command{}, fmt.Errorf("prefix: %w", err)
	}
	if err := validArgv(inner); err != nil {
		return Command{}, fmt.Errorf("inner command: %w", err)
	}
	c := Command{
		Argv: make([]string, 0, len(p.Prefix)+len(inner)),
		Env:  append([]string(nil), p.Env...),
	}
	c.Argv = append(c.Argv, p.Prefix...)
	c.Argv = append(c.Argv, inner...)
	if err := c.Validate(); err != nil {
		return Command{}, err
	}
	return c, nil
}

// Planner is how the exchange mirror and the intake reach a sandbox.
type Planner interface {
	// Serve returns the plan for serving a lead's clone.
	Serve(ServeRequest) (ServePlan, error)
	// Verify returns the plan for verifying a lead branch.
	Verify(VerifyRequest) (VerifyPlan, error)
}

// ShellQuote returns the argv as one POSIX shell line: each element is one
// word in single quotes, joined by single spaces. It refuses an empty argv
// and a NUL byte. Git runs its --upload-pack option through sh, so a caller
// passes the quoted argv there.
func ShellQuote(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	var b strings.Builder
	for i, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return "", fmt.Errorf("argv[%d] holds a NUL byte", i)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String(), nil
}

// ParseMeasure reads the output of a measure command: the first
// whitespace-delimited token of the first line is a decimal byte count of at
// most [MaxMeasureDigits] digits, with no sign. The rest of the output is
// ignored, as du prints the path after the count.
func ParseMeasure(out []byte) (int64, error) {
	if len(out) == 0 {
		return 0, errors.New("empty measure output")
	}
	if strings.IndexByte(string(out), 0) >= 0 {
		return 0, errors.New("measure output holds a NUL byte")
	}
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, errors.New("first line of the measure output is empty")
	}
	tok := fields[0]
	if len(tok) > MaxMeasureDigits {
		return 0, fmt.Errorf("measure is longer than %d digits", MaxMeasureDigits)
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, errors.New("measure is not a decimal number")
		}
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("measure: %w", err)
	}
	return n, nil
}

// ValidName reports whether s is a name for a role or a project:
// [a-z0-9][a-z0-9-]{0,62}.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
