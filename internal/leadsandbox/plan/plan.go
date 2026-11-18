package plan

import (
	"errors"
	"fmt"
	"path"
	"sort"
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
	//
	// Binding by descriptor does not change the reasoning. The host path of a
	// source Ataniq supplies is still written once: it moved from the
	// bubblewrap arguments (--ro-bind SRC DEST) to the pass-through step
	// (--walk-dir FD ROOT REL), where ROOT and REL together are as long as
	// SRC, and the bubblewrap arguments carry the number in its place
	// (--ro-bind-fd FD DEST). Per such row that adds the option word of the
	// walk and two numbers, under 40 bytes; with the most rows a profile can
	// have (see MaxPassFDs) under 4 KiB, a sixteenth of the cap.
	MaxArgvBytes = 64 << 10
	// MinSeccompFD is the lowest descriptor number the seccomp file may use:
	// 0, 1 and 2 are the standard streams.
	MinSeccompFD = 3
	// MaxSeccompFD is the highest descriptor number the seccomp file may use.
	MaxSeccompFD = 1023
	// MinReportFD and MaxReportFD bound the descriptor of the report channel.
	MinReportFD = 3
	MaxReportFD = 1023
	// MinBindFD is the lowest descriptor number the plan gives a bind source.
	// The numbers are the plan's own choice, not the caller's: the sources
	// that Ataniq supplies get the numbers from MinBindFD up, in the order of
	// the bind table, and a number that is the seccomp or the report
	// descriptor of the options is passed over, so the three kinds never
	// collide. The numbers start at 10 and not at 3 so that they stay clear of
	// the low numbers a caller picks for the other two, and of 3 and 4, which
	// the probe refuses as a target when its Go runtime holds them (a standard
	// stream that was non-blocking at its start).
	MinBindFD = 10
	// MaxBindFD is the highest descriptor number a bind source may get.
	MaxBindFD = 1023
	// MaxPassFDs is the most descriptors the pass-through step places, of
	// every kind together: the cap of the probe's pass-fds mode, which a test
	// of the probe compares with this number. A plan needs one per bind row
	// Ataniq supplies (23 in the bind table today), one per extra placeholder
	// (the profile allows 64), the seccomp file and the report channel: 89 at
	// most. 128 leaves room for rows the table gains. A plan above it is
	// refused rather than left to fail in the step.
	MaxPassFDs = 128
)

// The pass-through step: PROBE_HOST pass-fds [--read FD=PATH]... [--write
// FD=PATH]... [--walk-dir FD ROOT REL]... [--walk-file FD ROOT REL]... --
// COMMAND. The probe binary's pass-fds mode is the contract: it opens each
// PATH read-only (--read) or write-only without creating it (--write) at
// exactly the descriptor FD; it opens each ROOT without following a symbolic
// link, walks REL beneath that handle in one openat2 lookup that follows
// none, checks on the descriptor that the source is a folder (--walk-dir) or
// a regular file (--walk-file) and places that descriptor at exactly FD; it
// leaves every other descriptor above 2 closed, and executes the command
// after the "--". A source its walk refuses ends the step before the command.
const (
	// PassFDsMode is the mode argument of the probe binary.
	PassFDsMode = "pass-fds"
	// PassFDsRead and PassFDsWrite are its options for a file.
	PassFDsRead  = "--read"
	PassFDsWrite = "--write"
	// PassFDsWalkDir and PassFDsWalkFile are its options for a bind source
	// that is walked by handle: a folder, or a regular file.
	PassFDsWalkDir  = "--walk-dir"
	PassFDsWalkFile = "--walk-file"
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
	// NoteBindSources states rule G1 of the sandbox plan.
	NoteBindSources = "bind sources (rule G1): no source of a bind lies in or below a host folder the lead can write, because a source is resolved on the host, outside every sandbox, where a symbolic link would be followed; a folder the lead writes is bound whole from an Ataniq-owned parent, and the tree's .git/config, .git/hooks and .git/info are bound from Ataniq's copy of them; a source Ataniq supplies is its real path as the inputs name it and is bound from a descriptor (see the note on descriptors), and a row of the host (/usr, the /etc files) is bound by path, from the real path the holder resolved (Options.HostSources), which bubblewrap resolves by name; this package checks the text of the sources, and the holder checks each source without following links right before the start (mountpoints.CheckBindSources), as a check before the walk that counts"
	// NoteBindFDs states how the sources Ataniq supplies are bound (rule B1),
	// and what nothing here shows.
	NoteBindFDs = "bind sources by descriptor (rule B1): every bind source Ataniq supplies is bound from a descriptor (--bind-fd, --ro-bind-fd), never by path; the pass-fds step, the last host-side code before bubblewrap, opens the folder each walk starts from without following a symbolic link, walks the path beneath that handle in one openat2 lookup that follows none (RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS), checks on the descriptor it got that the source is the folder or the regular file the row binds, and places that descriptor at the number bubblewrap is given; a source its walk refuses ends the step before bubblewrap starts; whether bubblewrap mounts the object behind the descriptor, and closes the descriptor before the command inside starts, are qualification items that nothing here shows [qualify]"
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

	// HostSources gives, for each row of the host that the plan emits (a
	// read-only bind whose source role is profile.RoleHost: /usr, /etc/hosts,
	// /etc/localtime and the like, whose path equals its inside path), the
	// real path to bind in its place: the path the caller resolved with
	// mountpoints.ResolveSources ([mountpoints.ResolvedSource].Real; the
	// method Resolution.HostSources makes this map). The key is the row's
	// inside path. A row of the host is bound by path: bubblewrap binds the
	// path the plan names and follows a link in it, and the check that no
	// source has a link (mountpoints.CheckBindSources) opens the real path of
	// such a row, so the plan must bind that path: the path that is bound is
	// the path that is checked. Every host row the plan emits must have an entry
	// (a row that is not optional, or is optional and HostPresent says the
	// host has it); an entry for any other key, or for an optional row the
	// host lacks, is refused. A value is an absolute clean path, and is checked
	// like any other source (never-mounted paths, lead-writable folders).
	HostSources map[string]string

	// SourceWalks says, for each bind row that Ataniq supplies (every bind
	// row that is not a row of the host), how the pass-through step reaches
	// its source by handle: the folder to open by name, the path to walk
	// beneath it and the type the source must have. The caller takes them
	// from mountpoints.SourceWalks (mountpoints.PlanWalks gives them in this
	// type), which derives them from the inputs alone; they are data here, as
	// HostSources is, because this package does not import mountpoints.
	// There must be exactly one walk per such row, in any order: its Inside
	// is the row's inside path, its Root joined with its Rel is the row's
	// source, and its Dir says what the row binds. A walk for any other inside
	// path (a row of the host, a row without a source, no row), two walks for
	// one row, a row without a walk, a walk that does not lead to the source
	// or names the other type, a Root that lies in or below a folder the lead
	// can write, and a Root or Rel that is not a clean path are refused.
	SourceWalks []SourceWalk

	// SeccompFD is the descriptor number the pass-through step will open the
	// seccomp file at, from MinSeccompFD to MaxSeccompFD. It is required when
	// the inputs name a seccomp file and must be zero when they do not.
	SeccompFD int

	// ReportPath is the absolute host path the holder made for the report
	// channel: a FIFO or a file it owns, outside every lead-writable folder.
	// It is required. The pass-through step opens it write-only without
	// creating it; the probe inside writes its report line to it. It is
	// refused when it lies in or below a read-write source of the bind table
	// (a folder the lead writes) or a never-mounted path, when it is, lies
	// below or lies above any source of the bind table or the state root's
	// leads or mirrors folder, and when it is not directly inside ReportDir.
	ReportPath string
	// ReportDir is the folder the holder owns for report channels, an
	// absolute clean host path. It is required, and ReportPath must be a
	// direct child of it: a path that is not below a folder the holder named
	// as its own is refused, so a holder bug cannot point the step at a file
	// that the sandbox or another lead owns. This package does not look at the
	// folder.
	ReportDir string
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

// SourceWalk says how the source of one bind row that Ataniq supplies is
// reached by handle (rule B1 of the design, revision 4, section 3.6). It is
// the data of mountpoints.SourceWalk that the plan needs, and names paths
// only.
type SourceWalk struct {
	// Inside is the inside path of the row.
	Inside string
	// Root is the host folder the pass-through step opens by name, without
	// following a symbolic link in any component: absolute and clean. It is
	// a folder no sandbox can write: the state root for a source below it,
	// else the folder that holds the source.
	Root string
	// Rel is the path of the source beneath Root: relative and clean, without
	// "..", and "." only when the source is Root itself.
	Rel string
	// Dir says the source must be a folder; else it must be a regular file.
	Dir bool
}

// Path is the host path of the source: Root joined with Rel.
func (w SourceWalk) Path() string { return joinWalk(w.Root, w.Rel) }

// joinWalk joins the folder a walk starts from with the path beneath it.
func joinWalk(root, rel string) string {
	switch {
	case rel == ".":
		return root
	case root == "/":
		return "/" + rel
	}
	return root + "/" + rel
}

// Walk is a bind source the pass-through step of the plan walks to, with the
// descriptor number it places the source at, which is the number bubblewrap
// binds the row from.
type Walk struct {
	// FD is the descriptor number, from MinBindFD up.
	FD int
	// Inside is the inside path of the row that is bound from the descriptor.
	Inside string
	// Root and Rel are the folder the step opens and the path it walks
	// beneath it; Dir says the source must be a folder, else a regular file.
	Root string
	Rel  string
	Dir  bool
}

// Path is the host path of the source: Root joined with Rel.
func (w Walk) Path() string { return joinWalk(w.Root, w.Rel) }

// Mount is one emitted row of the bind table.
type Mount struct {
	// Kind is the row's kind (tmpfs, dev, proc, ro-bind, rw-bind, symlink).
	Kind profile.Kind
	// Inside is the path inside the sandbox.
	Inside string
	// Source is the host path of a bind row; empty for the other kinds.
	Source string
	// FD is the descriptor the row is bound from (--bind-fd, --ro-bind-fd): the
	// number of the row's entry in Plan.Walks. It is set for every bind row
	// Ataniq supplies and is zero for a row of the host, which is bound by
	// path, and for a row without a source.
	FD int
	// LeadWritable says the source lies below a host folder the lead can
	// write (the source of a read-write bind row): the lead could replace the
	// source with a symbolic link or with anything else. Rule G1 says no
	// source does, so it is false for every row of a valid plan: Build refuses
	// a table with such a row and Verify reports one (ProbLeadWritable). It
	// is not the profile's Bind.LeadWritable, which says the mount point (the
	// target) lies in such a folder, and which stays true for the mount points
	// the three .git rows have in the tree. A read-write bind's own source is
	// not marked: the lead cannot replace the folder it is bound to.
	LeadWritable bool
	// Optional says the row is optional in the table; the plan emitted it
	// because the options say the host has it.
	Optional bool
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
	// one --walk-dir FD ROOT REL or --walk-file FD ROOT REL per entry of
	// Walks, and "--".
	PassFDs []string
	// Bwrap is the bubblewrap executable and its arguments, ending in "--".
	Bwrap []string
	// Inner is the probe command line: /ataniq/probe --spec FILE --report-fd
	// FD -- AGENT...
	Inner []string

	// Env is the environment allowlist with values, sorted by name. The
	// bubblewrap arguments set these after --clearenv. Bubblewrap also keeps
	// and sets PWD (to the --chdir folder) under --clearenv, so the agent sees
	// Env plus PWD: use [InsideEnvironment] for what a check inside expects.
	Env []profile.EnvVar
	// Files are the descriptors the pass-through step opens, in the order of
	// PassFDs: the seccomp file when the inputs name one (read), then the
	// report channel (write).
	Files []File
	// Walks are the bind sources the pass-through step walks to by handle, in
	// the order of PassFDs, which is the order of the bind table: one per
	// bind row Ataniq supplies, with the descriptor number the row is bound
	// from. The step opens them before the files.
	Walks []Walk
	// Digest is the profile digest (profile.Inputs.Digest).
	Digest string
	// Mounts lists the rows of the bind table the plan emits, in order (not
	// the final read-only remount), with what the holder needs to re-check
	// them right before it starts the command.
	Mounts []Mount
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
	f, err := derive(in)
	if err != nil {
		return nil, err
	}
	workDir, err := opt.check(in, f)
	if err != nil {
		return nil, err
	}
	flags, env, binds, digest := f.flags, f.env, f.table, f.digest

	e := &emitter{}
	e.raw(flags...)
	for _, v := range env {
		e.opt("--setenv", v.Name, v.Value)
	}
	if in.SeccompEnabled() {
		e.opt("--seccomp", strconv.Itoa(opt.SeccompFD))
	}
	// Rule G1, checked on the table itself: the profile refuses such a table,
	// and the plan does not rely on that.
	if err := checkSourcesNotWritable(binds); err != nil {
		return nil, err
	}
	var mounts []Mount
	writable := writableSources(binds)
	walks := wantWalks(in, binds, opt)
	walkFD := map[string]int{}
	for _, w := range walks {
		walkFD[w.Inside] = w.FD
	}
	for _, b := range binds {
		if b.Kind == profile.KindRemountRO {
			continue // last, after --chdir
		}
		if b.Optional && !opt.HostPresent[b.Inside] {
			continue
		}
		fd := walkFD[b.Inside]
		if suppliedRow(b) && fd == 0 {
			// The options were checked, so every such row has a walk: a row
			// Ataniq supplies is never emitted by path.
			return nil, refuse("bind "+b.Inside, "the row has no descriptor to be bound from")
		}
		e.bind(b, fd)
		mounts = append(mounts, Mount{Kind: b.Kind, Inside: b.Inside, Source: b.Source, FD: fd, LeadWritable: belowAny(b.Source, writable), Optional: b.Optional})
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
		PassFDs: passFDs(in.ProbeBinary, files, walks),
		Bwrap:   append([]string{opt.BwrapPath}, e.args...),
		Inner:   probeCommand(opt),
		Env:     env,
		Mounts:  mounts,
		Files:   files,
		Walks:   walks,
		Digest:  digest,
		WorkDir: workDir,
		Notes:   notes(in, opt, workDir, leftOut(binds, opt)),
	}
	p.Argv = join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)
	if err := assertPlan(p, in, opt); err != nil {
		return nil, err
	}
	return p, nil
}

// assertPlan is Build's last word on its own result: the size cap, the
// forbidden options and Verify. Each of the first two is also a refusal that
// an earlier step (checkAgent, the emitter) reaches first for every input the
// package can see, so the tests call assertPlan on a tampered plan to show
// that it refuses on its own.
func assertPlan(p *Plan, in profile.Inputs, opt Options) error {
	if n := argvBytes(p.Argv); n > MaxArgvBytes {
		return refuse("command line", "is %d bytes, above the cap of %d", n, MaxArgvBytes)
	}
	if bad := forbiddenIn(p.Argv); bad != "" {
		return refuse("command line", "holds the forbidden option %s", bad)
	}
	if probs := Verify(p, in, opt); len(probs) > 0 {
		return refuse("plan", "fails its own check (%d problems, first: %s)", len(probs), probs[0])
	}
	return nil
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

// suppliedRow says a row of the table is a bind whose source Ataniq supplies:
// a read-only or read-write bind that is not a row of the host. Such a row is
// bound from a descriptor the pass-through step walked to, and has a walk.
func suppliedRow(b profile.Bind) bool {
	return (b.Kind == profile.KindRoBind || b.Kind == profile.KindRwBind) && !isHostRow(b)
}

// wantWalks lists what the pass-through step walks for these inputs and
// options, in the order of the table: one walk per bind row Ataniq supplies,
// from the walk the options give for it, with its descriptor number. The
// numbers are the lowest from MinBindFD up, a number that is the seccomp or
// the report descriptor of the options passed over, so they depend on the
// table and the options alone. The options must have passed check: a row the
// options give no walk for is left out.
func wantWalks(in profile.Inputs, table []profile.Bind, opt Options) []Walk {
	given := map[string]SourceWalk{}
	for _, w := range opt.SourceWalks {
		given[w.Inside] = w
	}
	taken := map[int]bool{opt.ReportFD: true}
	if in.SeccompEnabled() {
		taken[opt.SeccompFD] = true
	}
	var walks []Walk
	next := MinBindFD
	for _, b := range table {
		w, ok := given[b.Inside]
		if !suppliedRow(b) || !ok {
			continue
		}
		for taken[next] {
			next++
		}
		walks = append(walks, Walk{FD: next, Inside: b.Inside, Root: w.Root, Rel: w.Rel, Dir: w.Dir})
		next++
	}
	return walks
}

// passFDs is the pass-through step for the files, reads before writes (the
// order wantFiles gives), then the walks in the order wantWalks gives.
func passFDs(probeHost string, files []File, walks []Walk) []string {
	step := []string{probeHost, PassFDsMode}
	for _, f := range files {
		opt := PassFDsRead
		if f.Mode == FileWrite {
			opt = PassFDsWrite
		}
		step = append(step, opt, strconv.Itoa(f.FD)+"="+f.Path)
	}
	for _, w := range walks {
		opt := PassFDsWalkFile
		if w.Dir {
			opt = PassFDsWalkDir
		}
		step = append(step, opt, strconv.Itoa(w.FD), w.Root, w.Rel)
	}
	return append(step, "--")
}

// probeCommand is the probe command line inside.
func probeCommand(opt Options) []string {
	return append([]string{profile.ProbeInside, "--spec", profile.SpecFileInside, ProbeReportFD, strconv.Itoa(opt.ReportFD), "--"}, opt.AgentArgv...)
}

// facts are what the profile derives from the inputs: each call validates the
// inputs, so the plan asks once.
type facts struct {
	table  []profile.Bind
	flags  []string
	env    []profile.EnvVar
	digest string
}

// derive validates the inputs and derives their facts. The error is the
// profile's own refusal.
func derive(in profile.Inputs) (*facts, error) {
	var f facts
	var err error
	if f.table, err = in.Binds(); err != nil {
		return nil, err
	}
	if f.flags, err = in.Flags(); err != nil {
		return nil, err
	}
	if f.env, err = in.Environment(); err != nil {
		return nil, err
	}
	if f.digest, err = in.Digest(); err != nil {
		return nil, err
	}
	return &f, nil
}

// check validates the options against the inputs and returns the working
// folder. It also puts the host sources of the options into the table of f, so
// that every later step (the report path check, the emitted mounts, Verify)
// works on the paths that are bound.
func (o Options) check(in profile.Inputs, f *facts) (string, error) {
	if err := o.applyHostSources(in, f); err != nil {
		return "", err
	}
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
	if err := o.checkReport(in, f.table); err != nil {
		return "", err
	}
	if err := o.checkWalks(in, f.table); err != nil {
		return "", err
	}
	for _, e := range f.env {
		if e.Name == "PWD" {
			return "", refuse("environment", "PWD is in the allowlist; bubblewrap sets PWD itself, to the working folder")
		}
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

// isHostRow says a row of the table is a bind of the host's own path.
func isHostRow(b profile.Bind) bool {
	return b.Kind == profile.KindRoBind && b.SourceRole == profile.RoleHost
}

// applyHostSources replaces, in the table of f, the source of each host row the
// plan emits by the real path the options give for it, and refuses options
// that give none for such a row, give one for a key that is not an emitted
// host row, give a path that is not an absolute clean one, or give one a source
// may not have (a never-mounted path and the like). The table of
// the profile, which names the host rows by their own paths, is not changed.
func (o Options) applyHostSources(in profile.Inputs, f *facts) error {
	table := append([]profile.Bind(nil), f.table...)
	index := map[string]int{}
	for i, b := range table {
		if isHostRow(b) {
			index[b.Inside] = i
		}
	}
	keys := make([]string, 0, len(o.HostSources))
	for k := range o.HostSources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i, ok := index[k]
		switch {
		case !ok:
			return refuse("host sources", "%q is not the inside path of a row of the host in the bind table", clip(k))
		case table[i].Optional && !o.HostPresent[k]:
			return refuse("host sources", "%s is an optional row that the options say the host lacks, and has a source", k)
		}
		if err := checkPath("host source of "+k, o.HostSources[k]); err != nil {
			return err
		}
		// A real path is held to the rules of any other source: never-mounted
		// paths, the other runtime's config folder and the rules about other
		// state (other leads, the holder folder, other projects' mirrors and
		// published folders, the mailboxes, the coordinators' trees, homes and
		// logins). (Verify checks every mount again.)
		if err := in.CheckSource(o.HostSources[k]); err != nil {
			var r *profile.Refusal
			if errors.As(err, &r) {
				return refuse("host source of "+k, "%s", r.Reason)
			}
			return err
		}
		table[i].Source = o.HostSources[k]
	}
	for _, b := range table {
		if !isHostRow(b) || b.Optional && !o.HostPresent[b.Inside] {
			continue
		}
		if _, ok := o.HostSources[b.Inside]; !ok {
			return refuse("host sources", "the row of the host at %s has no resolved source: bubblewrap binds the path the plan names and the check of the bind sources opens the real path, so the caller gives the real path it resolved", b.Inside)
		}
	}
	f.table = table
	return nil
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
		// An argument after the probe's -- is never read by bubblewrap, so
		// this over-refuses on purpose: defence in depth, so that no element
		// of the whole command line equals a forbidden option.
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
func (o Options) checkReport(in profile.Inputs, table []profile.Bind) error {
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
	if o.ReportDir == "" {
		return refuse("report folder", "missing: the report path must be directly inside a folder the holder owns, and the caller names it")
	}
	if err := checkPath("report folder", o.ReportDir); err != nil {
		return err
	}
	for _, d := range writableSources(table) {
		if within(o.ReportPath, d) {
			return refuse("report path", "lies in or below %s, a folder the lead can write", d)
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
	// The step opens the path write-only: a path that is, lies below or lies
	// above a source of the table is a file the sandbox sees (the spec, the
	// managed settings, the probe) or a folder it is built from.
	for _, b := range table {
		if b.Source != "" && (within(o.ReportPath, b.Source) || within(b.Source, o.ReportPath)) {
			return refuse("report path", "is, lies below or lies above the source of the bind at %s", b.Inside)
		}
	}
	l := in.Layout()
	for _, d := range []string{path.Join(l.StateRoot, "leads"), l.MirrorsDir(), l.MailboxesDir(), l.PublishedDir(), l.CoordsDir(), l.CoordLoginDir()} {
		if within(o.ReportPath, d) || within(d, o.ReportPath) {
			return refuse("report path", "is, lies below or lies above the state folder %s, which holds leads', roles', coordinators' and projects' files", d)
		}
	}
	if o.ReportPath == o.ReportDir || path.Dir(o.ReportPath) != o.ReportDir {
		return refuse("report path", "is not directly inside the report folder %s that the holder owns", o.ReportDir)
	}
	return nil
}

// checkWalks checks the walks of the options against the table: exactly one
// walk per bind row Ataniq supplies, which leads to the row's source and names
// the type the row binds, and none for anything else. It is the text of the
// walks that is checked: that Root is a folder no sandbox can write is the
// caller's to see to, and the package refuses only the Root it can see is
// wrong, one in or below a folder the lead writes. It also holds the number of
// descriptors to the cap of the pass-through step.
func (o Options) checkWalks(in profile.Inputs, table []profile.Bind) error {
	const field = "source walks"
	rows := map[string]profile.Bind{}
	supplied := 0
	for _, b := range table {
		if suppliedRow(b) {
			rows[b.Inside] = b
			supplied++
		}
	}
	writable := writableSources(table)
	seen := map[string]bool{}
	for n, w := range o.SourceWalks {
		b, ok := rows[w.Inside]
		switch {
		case !ok:
			return refuse(field, "walk %d is for %q, which is not the inside path of a bind row Ataniq supplies (a row of the host is bound by path and has no walk)", n+1, clip(w.Inside))
		case seen[w.Inside]:
			return refuse(field, "the row at %s has two walks", w.Inside)
		}
		seen[w.Inside] = true
		what := "walk of " + w.Inside
		if err := checkPath("folder of the "+what, w.Root); err != nil {
			return err
		}
		if err := checkRel("path of the "+what, w.Rel, w.Dir); err != nil {
			return err
		}
		if w.Path() != b.Source {
			return refuse(field, "the walk of %s does not lead to the source of the row: the folder and the path beneath it must name %s", w.Inside, b.Source)
		}
		if w.Dir != b.IsDir {
			return refuse(field, "the walk of %s is for %s and the row binds %s", w.Inside, kindWord(w.Dir), kindWord(b.IsDir))
		}
		for _, d := range writable {
			if within(w.Root, d) {
				return refuse(field, "the walk of %s starts from %s, which is, or lies below, %s, a folder the lead can write: a walk starts from a folder no sandbox can write (rule B1)", w.Inside, w.Root, d)
			}
		}
	}
	for _, b := range table {
		if suppliedRow(b) && !seen[b.Inside] {
			return refuse(field, "the row at %s has no walk: every bind source Ataniq supplies is bound from a descriptor the pass-through step walked to, so the caller gives the walk of each (mountpoints.SourceWalks)", b.Inside)
		}
	}
	files := 1
	if in.SeccompEnabled() {
		files++
	}
	return checkFDCount(supplied, files)
}

// checkFDCount holds the descriptors of a plan to the cap of the pass-through
// step and to the numbers a bind source may get. A valid profile stays under
// both (see MaxPassFDs); the check is here so that the plan does not rely on
// it.
func checkFDCount(walks, files int) error {
	if n := walks + files; n > MaxPassFDs {
		return refuse("pass-through step", "would place %d descriptors (%d bind sources and %d files), above its cap of %d", n, walks, files, MaxPassFDs)
	}
	// The numbers run from MinBindFD up and pass over at most two.
	if last := MinBindFD + walks + 1; last > MaxBindFD {
		return refuse("pass-through step", "%d bind sources need descriptor numbers above %d", walks, MaxBindFD)
	}
	return nil
}

// kindWord names what a walk or a row binds.
func kindWord(dir bool) string {
	if dir {
		return "a folder"
	}
	return "a regular file"
}

// checkRel refuses the path of a walk beneath its folder unless it is relative
// and clean, without "..", free of control characters and no longer than a
// path may be; "." names the folder itself and is accepted for a folder only.
// A path that starts with "--" is refused too: it is an operand of the
// pass-through step, and the plan keeps every operand unlike an option so that
// its parse stays unambiguous.
func checkRel(field, rel string, dir bool) error {
	switch {
	case rel == "":
		return refuse(field, "is empty")
	case rel == ".":
		if !dir {
			return refuse(field, "is \".\", which names the folder and not a file")
		}
		return nil
	case strings.HasPrefix(rel, "/"):
		return refuse(field, "is absolute: it is the path beneath the folder")
	case strings.HasPrefix(rel, "--"):
		return refuse(field, "starts with --, which the plan refuses so that its parse stays unambiguous")
	}
	// The rest is the rule of any path of the profile, on the path as it
	// would lie below the root: clean, so without ".." and ".", and bounded.
	if err := profile.CheckPath(field, "/"+rel); err != nil {
		var r *profile.Refusal
		if errors.As(err, &r) {
			return refuse(field, "%s", r.Reason)
		}
		return err
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

// writableSources are the host folders the lead can write: the sources of the
// read-write rows of the table.
func writableSources(table []profile.Bind) []string {
	var dirs []string
	for _, b := range table {
		if b.Kind == profile.KindRwBind {
			dirs = append(dirs, b.Source)
		}
	}
	return dirs
}

// srcRow is the part of a bind row that rule G1 looks at: the plan's table and
// the mounts parsed back from its arguments both reduce to it.
type srcRow struct {
	kind   profile.Kind
	inside string
	source string
}

// writableSourceRows returns, for each row whose source is, or lies below, the
// source of a read-write row other than itself, the index of the row and the
// index of that read-write row (a host folder the lead can write). A row
// without a source is never one.
func writableSourceRows(rows []srcRow) (bad, folder []int) {
	for i, r := range rows {
		if r.source == "" {
			continue
		}
		for j, w := range rows {
			if j == i || w.kind != profile.KindRwBind || w.source == "" {
				continue
			}
			if within(r.source, w.source) {
				bad, folder = append(bad, i), append(folder, j)
				break
			}
		}
	}
	return bad, folder
}

// checkSourcesNotWritable refuses a table with a row whose source lies in or
// below a host folder the lead can write (rule G1).
func checkSourcesNotWritable(table []profile.Bind) error {
	rows := make([]srcRow, len(table))
	for i, b := range table {
		rows[i] = srcRow{b.Kind, b.Inside, b.Source}
	}
	if bad, folder := writableSourceRows(rows); len(bad) > 0 {
		return refuse("bind "+rows[bad[0]].inside, "the source %s lies in or below %s, a folder the lead can write (rule G1: a source the lead can replace with a symbolic link would be followed on the host)", rows[bad[0]].source, rows[folder[0]].source)
	}
	return nil
}

// belowAny reports whether p lies strictly below one of dirs. An empty p (a
// row without a source) lies below none.
func belowAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if p != "" && strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
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

// NotePWD is the note about the variable bubblewrap sets itself: --clearenv
// unsets every variable except PWD, and bubblewrap sets PWD to the working
// folder, so the environment inside is the allowlist plus PWD.
func NotePWD(workDir string) string {
	return fmt.Sprintf("bubblewrap keeps and sets PWD under --clearenv: the agent's environment is the allowlist plus PWD=%s, the working folder", workDir)
}

// InsideEnvironment is the environment the agent is expected to see: the
// allowlist of the plan plus PWD, which bubblewrap sets to the working folder,
// sorted by name. The start-up assertion compares what it observes with this
// list, not with Plan.Env alone.
func InsideEnvironment(p *Plan) []profile.EnvVar {
	env := append([]profile.EnvVar(nil), p.Env...)
	env = append(env, profile.EnvVar{Name: "PWD", Value: p.WorkDir})
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })
	return env
}

// noteReport is the note about the report channel.
func noteReport(fd int) string {
	return fmt.Sprintf("report channel: the pass-fds step opens the holder's report path write-only as descriptor %d after pasta, bubblewrap inherits it and the probe inside gets --report-fd %d; the holder takes one accepted probe line as the positive signal and does not wait for end-of-file, because bubblewrap's own processes may keep the descriptor open, and a second line or any further byte after the line is a failure of the start; whether the descriptor reaches the probe inside and which other processes hold it are qualification items that nothing here shows", fd, fd)
}

func notes(in profile.Inputs, opt Options, workDir string, left []string) []string {
	n := []string{NoteShareNet, NoteNestedUserns, NoteBindSources, NoteBindFDs}
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
	n = append(n, noteReport(opt.ReportFD), NotePWD(workDir))
	n = append(n, "network: pasta arguments of sandboxnet.PastaArgs (outbound NAT, no port forwarding); outbound traffic is not narrowed", NoteSession)
	return append(n, left...)
}
