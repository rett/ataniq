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
	// argument counted with its terminating NUL. Linux shares one budget
	// between the arguments and the environment of a new program (at least 32
	// pages, else a quarter of the stack limit) and the plan cannot see the
	// environment the launcher passes, so the cap stays well under that
	// minimum. A plan above it is refused rather than left to fail at exec.
	MaxArgvBytes = 64 << 10
	// MinSeccompFD is the lowest descriptor number the seccomp file may use:
	// 0, 1 and 2 are the standard streams.
	MinSeccompFD = 3
	// MaxSeccompFD is the highest descriptor number the seccomp file may use.
	MaxSeccompFD = 1023
	// MinReportFD and MaxReportFD bound the descriptor of the report channel.
	MinReportFD = 3
	MaxReportFD = 1023
)

// The pass-through step: PROBE_HOST pass-fds [--read FD=PATH]... [--write
// FD=PATH]... -- COMMAND. The probe binary's pass-fds mode is the contract:
// it opens each PATH read-only (--read) or write-only without creating it
// (--write) at exactly the descriptor FD, leaves every other descriptor above
// 2 closed, and executes the command after the "--".
const (
	// PassFDsMode is the mode argument of the probe binary.
	PassFDsMode = "pass-fds"
	// PassFDsRead and PassFDsWrite are its options.
	PassFDsRead  = "--read"
	PassFDsWrite = "--write"
	// ProbeReportFD is the probe's option for the report descriptor.
	ProbeReportFD = "--report-fd"
)

// The purposes of the descriptors of Plan.Files.
const (
	PurposeSeccomp = "seccomp"
	PurposeReport  = "report"
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

	// SeccompFD is the descriptor number the pass-through step will open the
	// seccomp file at, from MinSeccompFD to MaxSeccompFD. It is required when
	// the inputs name a seccomp file and must be zero when they do not.
	SeccompFD int

	// ReportPath is the absolute host path the holder made for the report
	// channel: a FIFO or a file it owns, outside every lead-writable folder.
	// It is required. The pass-through step opens it write-only without
	// creating it; the probe inside writes its report line to it. It is
	// refused when it lies in or below a read-write source of the bind table
	// (a folder the lead writes) or a never-mounted path.
	ReportPath string
	// ReportFD is the descriptor number of the report channel, from
	// MinReportFD to MaxReportFD and different from SeccompFD. It is required.
	ReportFD int
}

// FileMode says how the pass-through step opens a file.
type FileMode string

// The two ways.
const (
	// FileRead opens the path read-only (--read).
	FileRead FileMode = "read"
	// FileWrite opens the path write-only without creating it (--write).
	FileWrite FileMode = "write"
)

// File is a descriptor the pass-through step of the plan opens, at the number
// FD, before it executes bubblewrap. The launcher opens nothing: pasta closes
// every descriptor it inherits, so only a process that pasta starts can open
// one for bubblewrap.
type File struct {
	FD int
	// Path is the host path the step opens.
	Path string
	// Mode says whether it is opened read-only or write-only.
	Mode FileMode
	// Purpose says what the descriptor is for (PurposeSeccomp, PurposeReport).
	Purpose string
	// SHA256 is the digest the caller gave for the seccomp file; empty for
	// the report channel. This package does not read the file; the holder may
	// compare the digest before the start.
	SHA256 string
}

// Plan is a launch plan. Argv is Pasta, PassFDs, Bwrap and Inner joined.
type Plan struct {
	// Argv is the whole command: pasta, its arguments, "--", the pass-through
	// step, "--", bwrap, its arguments, "--", the probe command line.
	Argv []string
	// Pasta is the pasta executable and its arguments, ending in "--".
	Pasta []string
	// PassFDs is the pass-through step: the host path of the probe binary,
	// "pass-fds", one --read FD=PATH or --write FD=PATH per entry of Files,
	// and "--".
	PassFDs []string
	// Bwrap is the bubblewrap executable and its arguments, ending in "--".
	Bwrap []string
	// Inner is the probe command line: /ataniq/probe --spec FILE --report-fd
	// FD -- AGENT...
	Inner []string

	// Env is the environment allowlist with values, sorted by name. The
	// bubblewrap arguments set exactly these, after --clearenv.
	Env []profile.EnvVar
	// Files are the descriptors the pass-through step opens, in the order of
	// PassFDs: the seccomp file when the inputs name one (read), then the
	// report channel (write).
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
	if in.SeccompEnabled() {
		e.opt("--seccomp", strconv.Itoa(opt.SeccompFD))
	}
	for _, b := range binds {
		if b.Kind == profile.KindRemountRO {
			continue // last, after --chdir
		}
		if b.Optional && !opt.HostPresent[b.Inside] {
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

	files := wantFiles(in, opt)
	p := &Plan{
		Pasta:   append([]string{opt.PastaPath}, sandboxnet.PastaArgs()...),
		PassFDs: passFDs(in.ProbeBinary, files),
		Bwrap:   append([]string{opt.BwrapPath}, e.args...),
		Inner:   probeCommand(opt),
		Env:     env,
		Files:   files,
		Digest:  digest,
		WorkDir: workDir,
		Notes:   notes(in, opt, leftOut(binds, opt)),
	}
	p.Argv = join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)
	if n := argvBytes(p.Argv); n > MaxArgvBytes {
		return nil, refuse("command line", "is %d bytes, above the cap of %d", n, MaxArgvBytes)
	}
	if bad := forbiddenIn(p.Argv); bad != "" {
		return nil, refuse("command line", "holds the forbidden option %s", bad)
	}
	if probs := Verify(p, in, opt); len(probs) > 0 {
		return nil, refuse("plan", "fails its own check (%d problems, first: %s)", len(probs), probs[0])
	}
	return p, nil
}

// wantFiles lists what the pass-through step opens for these inputs and
// options: the seccomp file (read) when the inputs name one, then the report
// channel (write).
func wantFiles(in profile.Inputs, opt Options) []File {
	var files []File
	if in.SeccompEnabled() {
		files = append(files, File{FD: opt.SeccompFD, Path: in.SeccompFile, Mode: FileRead, Purpose: PurposeSeccomp, SHA256: in.SeccompSHA256})
	}
	return append(files, File{FD: opt.ReportFD, Path: opt.ReportPath, Mode: FileWrite, Purpose: PurposeReport})
}

// passFDs is the pass-through step for the files, reads before writes (the
// order wantFiles gives).
func passFDs(probeHost string, files []File) []string {
	step := []string{probeHost, PassFDsMode}
	for _, f := range files {
		opt := PassFDsRead
		if f.Mode == FileWrite {
			opt = PassFDsWrite
		}
		step = append(step, opt, strconv.Itoa(f.FD)+"="+f.Path)
	}
	return append(step, "--")
}

// probeCommand is the probe command line inside.
func probeCommand(opt Options) []string {
	return append([]string{profile.ProbeInside, "--spec", profile.SpecFileInside, ProbeReportFD, strconv.Itoa(opt.ReportFD), "--"}, opt.AgentArgv...)
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
	if err := o.checkReport(in); err != nil {
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

// checkReport checks the report channel: its descriptor and its path.
func (o Options) checkReport(in profile.Inputs) error {
	switch {
	case o.ReportFD == 0:
		return refuse("report descriptor", "missing: the report channel is always present, so the descriptor number is required")
	case o.ReportFD < MinReportFD || o.ReportFD > MaxReportFD:
		return refuse("report descriptor", "%d is outside %d to %d (0 to 2 are the standard streams)", o.ReportFD, MinReportFD, MaxReportFD)
	case in.SeccompEnabled() && o.ReportFD == o.SeccompFD:
		return refuse("report descriptor", "%d is the seccomp descriptor; they must differ", o.ReportFD)
	}
	if err := checkPath("report path", o.ReportPath); err != nil {
		return err
	}
	table, err := in.Binds()
	if err != nil {
		return err
	}
	for _, b := range table {
		if b.Kind == profile.KindRwBind && within(o.ReportPath, b.Source) {
			return refuse("report path", "lies in or below %s, a folder the lead can write", b.Source)
		}
	}
	never := append([]string(nil), in.NeverMounted...)
	if other := in.OtherConfigDir(); other != "" {
		never = append(never, other)
	}
	for _, n := range never {
		if within(o.ReportPath, n) {
			return refuse("report path", "lies in or below the never-mounted path %s", n)
		}
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

// leftOut is the note of each optional row the options say the host lacks.
func leftOut(binds []profile.Bind, opt Options) []string {
	var left []string
	for _, b := range binds {
		if b.Optional && !opt.HostPresent[b.Inside] {
			left = append(left, optionalNote(b))
		}
	}
	return left
}

func optionalNote(b profile.Bind) string {
	what := "read-only bind"
	if b.Kind == profile.KindSymlink {
		what = "symlink"
	}
	return fmt.Sprintf("optional %s %s left out: the caller says the host has no such path", what, b.Inside)
}

// noteReport is the note about the report channel.
func noteReport(fd int) string {
	return fmt.Sprintf("report channel: the pass-fds step opens the holder's report path write-only as descriptor %d after pasta, bubblewrap inherits it and the probe inside gets --report-fd %d; whether bubblewrap keeps an inherited descriptor open for its child is a qualification item that nothing here shows", fd, fd)
}

func notes(in profile.Inputs, opt Options, left []string) []string {
	n := []string{NoteShareNet, NoteNestedUserns}
	if in.SeccompEnabled() {
		n = append(n, fmt.Sprintf("seccomp file named by the caller (sha256 %s), opened read-only as descriptor %d by the pass-fds step after pasta and passed to bubblewrap with --seccomp: this package neither reads nor generates a filter", in.SeccompSHA256, opt.SeccompFD))
	} else {
		n = append(n, NoteNoSeccomp)
	}
	if in.Identity == profile.IdentityOwner {
		n = append(n, fmt.Sprintf("identity: the owner's ids %d:%d inside (the fallback mode)", in.OwnerUID, in.OwnerGID))
	} else {
		n = append(n, "identity: uid and gid 0 inside, mapped to the owner outside; tools that run as UID 0 are untested")
	}
	n = append(n, noteReport(opt.ReportFD))
	n = append(n, "network: pasta arguments of sandboxnet.PastaArgs (outbound NAT, no port forwarding); outbound traffic is not narrowed", NoteSession)
	return append(n, left...)
}
