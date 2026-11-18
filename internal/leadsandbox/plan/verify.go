package plan

import (
	"fmt"
	"strconv"
	"strings"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

// ProblemKind names what a Problem is about.
type ProblemKind string

// The kinds of problem Verify reports.
const (
	ProbInputs       ProblemKind = "inputs"           // the profile inputs are refused
	ProbOptions      ProblemKind = "options"          // the options are refused
	ProbShape        ProblemKind = "shape"            // a plan with missing or inconsistent parts
	ProbExecutable   ProblemKind = "executable"       // pasta or bubblewrap path is not the one of the options
	ProbPasta        ProblemKind = "pasta"            // pasta arguments differ from sandboxnet.PastaArgs
	ProbPassFDs      ProblemKind = "pass-fds"         // the pass-through step or the list of descriptors
	ProbReport       ProblemKind = "report"           // the report channel
	ProbForbidden    ProblemKind = "forbidden-flag"   // an option of profile.ForbiddenFlags
	ProbUnknown      ProblemKind = "unknown-option"   // a bubblewrap option the plan never uses
	ProbMalformed    ProblemKind = "malformed"        // a missing operand or terminator
	ProbOptionLike   ProblemKind = "option-like"      // an operand that starts with --
	ProbOrder        ProblemKind = "order"            // sections or rows out of order
	ProbFlags        ProblemKind = "flags"            // the flag set differs from the profile's
	ProbEnv          ProblemKind = "env"              // the environment is not the allowlist
	ProbSeccomp      ProblemKind = "seccomp"          // --seccomp or its descriptor in Files
	ProbMountMissing ProblemKind = "mount-missing"    // a table row is not mounted
	ProbMountExtra   ProblemKind = "mount-unexpected" // a mount that is not in the table
	ProbMountDiffers ProblemKind = "mount-differs"    // a row with another kind, source or target
	ProbMountTwice   ProblemKind = "mount-twice"      // one inside path mounted twice
	ProbMountList    ProblemKind = "mount-list"       // Plan.Mounts is not what the arguments mount
	ProbNever        ProblemKind = "never-mounted"    // a source in or around a never-mounted path
	ProbOtherRuntime ProblemKind = "other-runtime"    // the other runtime's config folder
	ProbLeadWritable ProblemKind = "lead-writable"    // a bind source in or below a host folder the lead can write (rule G1)
	ProbBindByPath   ProblemKind = "bind-by-path"     // a bind row Ataniq supplies is bound by path, not from a descriptor (rule B1)
	ProbBindFD       ProblemKind = "bind-fd"          // a bind descriptor that is no number, is used twice, has no walk or no mount, or is one of another kind's
	ProbWalk         ProblemKind = "walk"             // a walk, or the list of walks, is not what the inputs and options give
	ProbWorkDir      ProblemKind = "workdir"          // --chdir or the plan's working folder is not the options'
	ProbRemount      ProblemKind = "remount-ro"       // --remount-ro / missing or not last
	ProbInner        ProblemKind = "inner"            // the probe command line
	ProbAgent        ProblemKind = "agent"            // the agent argv differs from the options
	ProbDigest       ProblemKind = "digest"           // not the profile digest
	ProbNotes        ProblemKind = "notes"            // the notes are not the exact set the inputs and options give
	ProbTerminal     ProblemKind = "terminal"         // SetControllingTerminal is true
	ProbSize         ProblemKind = "size"             // the command line is above the cap
)

// Problem is one thing wrong with a plan. Detail says what is wrong with
// names, kinds, positions and counts, and with values the profile or the
// options give; it never repeats text taken from the plan's own arguments (an
// environment name or value, a mount path, the agent argv, a token that is not
// an option the plan uses), because a tampered plan may hold a secret there.
type Problem struct {
	Kind   ProblemKind
	Detail string
}

// String is the problem as one line.
func (p Problem) String() string { return string(p.Kind) + ": " + p.Detail }

// bwrap options and the number of operands each takes.
var bwrapArity = map[string]int{
	"--die-with-parent": 0, "--unshare-all": 0, "--share-net": 0, "--unshare-user": 0,
	"--clearenv": 0, "--uid": 1, "--gid": 1, "--cap-drop": 1,
	"--setenv": 2, "--seccomp": 1,
	"--tmpfs": 1, "--dev": 1, "--proc": 1, "--ro-bind": 2, "--bind": 2, "--symlink": 2,
	"--ro-bind-fd": 2, "--bind-fd": 2,
	"--chdir": 1, "--remount-ro": 1,
}

// Sections of the bubblewrap arguments, in the order they must come.
const (
	secFlags = iota
	secEnv
	secSeccomp
	secMount
	secChdir
	secRemount
)

func sectionOf(opt string) int {
	switch opt {
	case "--setenv":
		return secEnv
	case "--seccomp":
		return secSeccomp
	case "--tmpfs", "--dev", "--proc", "--ro-bind", "--bind", "--symlink", "--ro-bind-fd", "--bind-fd":
		return secMount
	case "--chdir":
		return secChdir
	case "--remount-ro":
		return secRemount
	}
	return secFlags
}

// mountKinds maps a mount option to the row kind it must come from.
var mountKinds = map[string]profile.Kind{
	"--tmpfs": profile.KindTmpfs, "--dev": profile.KindDev, "--proc": profile.KindProc,
	"--ro-bind": profile.KindRoBind, "--bind": profile.KindRwBind, "--symlink": profile.KindSymlink,
	"--ro-bind-fd": profile.KindRoBind, "--bind-fd": profile.KindRwBind,
}

// item is one parsed option with its operands.
type item struct {
	opt  string
	args []string
}

// mount is one parsed mount option as a row.
type mount struct {
	kind           profile.Kind
	source, target string // target is the link target of a symlink
	inside         string
	// byFD says the option binds from a descriptor (--bind-fd, --ro-bind-fd);
	// fdText is its first operand as written. fd is that number when it is
	// one, else zero. walk is the walk of the pass-through step that opens the
	// descriptor, when exactly one does: source is then the path that walk
	// leads to, and is empty otherwise.
	byFD   bool
	fdText string
	fd     int
	walk   *stepWalk
}

// stepWalk is one walk option parsed back from the pass-through step.
type stepWalk struct {
	// fd is the descriptor number, or zero when the operand is not one.
	fd        int
	root, rel string
	dir       bool
	// used counts the mounts bound from the descriptor.
	used int
}

// parseStepWalks reads the walk options of the pass-through step back, apart
// from the code that writes them: after the program and the mode word come
// options, a file option with one operand and a walk option with three, up to
// the final "--". It returns the walks it could read, in order; what it cannot
// read is left to the comparison of the whole step with the expected one, which
// reports it.
func parseStepWalks(step []string) []*stepWalk {
	var walks []*stepWalk
	for i := 2; i < len(step); {
		switch step[i] {
		case PassFDsRead, PassFDsWrite:
			i += 2
		case PassFDsWalkDir, PassFDsWalkFile:
			if i+3 >= len(step) {
				return walks
			}
			walks = append(walks, &stepWalk{fd: fdNumber(step[i+1]), root: step[i+2], rel: step[i+3], dir: step[i] == PassFDsWalkDir})
			i += 4
		default:
			return walks
		}
	}
	return walks
}

// fdNumber reads a descriptor number as the probe does: decimal digits, no
// sign and no leading zero, from 3 to 1023. Anything else is zero.
func fdNumber(s string) int {
	if s == "" || len(s) > 4 || s[0] == '0' {
		return 0
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	if n < minFD || n > MaxBindFD {
		return 0
	}
	return n
}

// minFD is the lowest descriptor number that is not a standard stream.
const minFD = 3

// Verify checks a finished plan against the profile inputs and the options the
// holder uses now, without trusting the code that built it. The holder gives
// what it knows independently of the plan: both executables, the agent argv,
// the working folder, the descriptor numbers and paths, and which optional
// host rows exist. Verify parses the bubblewrap arguments back into flags,
// environment and mounts and compares everything with the profile and the
// options: every part of the plan (the pasta, pass-through, bubblewrap and
// probe command lines, Argv, Env, Mounts, Files, Walks, Digest, Notes, WorkDir
// and SetControllingTerminal) is compared with what the inputs and the options
// give. It returns no problem for a plan that agrees with them. An optional
// row is present exactly when the options say the host has it; a row that is
// not optional is always present.
//
// A mount that binds from a descriptor (--bind-fd, --ro-bind-fd) is paired
// with the walk of the pass-through step that opens that descriptor, read back
// from the step itself, and the path that walk leads to is the mount's source
// for every check a source gets. A bind row Ataniq supplies that is bound by
// path is reported ([ProbBindByPath]); so are a descriptor that two mounts or
// two walks use, a mount without a walk and a walk without a mount, a row of
// the host bound from a descriptor, a bind descriptor that is the seccomp or
// the report descriptor ([ProbBindFD]); and a walk whose descriptor, folder,
// path or type is not the one the inputs and the options give ([ProbWalk]).
//
// The holder calls it before every start. It checks the text of the plan; it
// does not check that a source exists, nor what bubblewrap or pasta do with
// the arguments.
func Verify(p *Plan, in profile.Inputs, opt Options) []Problem {
	v := &verifier{}
	f, err := derive(in)
	if err != nil {
		v.add(ProbInputs, "%s", err)
		return v.out
	}
	if p == nil {
		v.add(ProbShape, "there is no plan")
		return v.out
	}
	workDir, err := opt.check(in, f)
	if err != nil {
		v.add(ProbOptions, "%s", err)
		return v.out
	}
	v.shape(p)
	v.pasta(p, opt)
	v.passFDs(p, in, opt, f)
	v.inner(p, opt)
	v.size(p)
	if bad := forbiddenIn(p.Argv); bad != "" {
		v.add(ProbForbidden, "%s appears in the command line", bad)
	}
	// The parts and Argv are compared by shape; this names a forbidden option
	// in a part even when Argv has been left unchanged.
	if bad := forbiddenIn(join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)); bad != "" && forbiddenIn(p.Argv) == "" {
		v.add(ProbForbidden, "%s appears in a part of the plan", bad)
	}
	v.bwrap(p, in, opt, f, workDir)
	v.rest(p, in, opt, f, workDir)
	return v.out
}

type verifier struct{ out []Problem }

func (v *verifier) add(k ProblemKind, format string, args ...any) {
	v.out = append(v.out, Problem{Kind: k, Detail: fmt.Sprintf(format, args...)})
}

// shape checks that Argv is the three parts joined.
func (v *verifier) shape(p *Plan) {
	if len(p.Pasta) == 0 || len(p.PassFDs) == 0 || len(p.Bwrap) == 0 || len(p.Inner) == 0 {
		v.add(ProbShape, "a part of the plan is empty")
	}
	joined := join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)
	if len(joined) != len(p.Argv) {
		v.add(ProbShape, "Argv has %d elements, its parts %d", len(p.Argv), len(joined))
		return
	}
	for i := range joined {
		if joined[i] != p.Argv[i] {
			v.add(ProbShape, "Argv differs from its parts at element %d", i)
			return
		}
	}
}

func (v *verifier) pasta(p *Plan, opt Options) {
	if len(p.Pasta) == 0 {
		return
	}
	if p.Pasta[0] != opt.PastaPath {
		v.add(ProbExecutable, "the pasta executable is not the one of the options")
	}
	want := sandboxnet.PastaArgs()
	got := p.Pasta[1:]
	if !equal(got, want) {
		v.add(ProbPasta, "the pasta arguments are not the %d of sandboxnet.PastaArgs", len(want))
	}
}

// passFDs checks the pass-through step and the list of descriptors against
// what the inputs and the options ask for.
func (v *verifier) passFDs(p *Plan, in profile.Inputs, opt Options, f *facts) {
	want := wantFiles(in, opt)
	before := len(v.out)
	for _, w := range want {
		kind := ProbReport
		if w.Purpose == PurposeSeccomp {
			kind = ProbSeccomp
		}
		var got *File
		for i := range p.Files {
			if p.Files[i].Purpose == w.Purpose {
				if got != nil {
					v.add(kind, "Files lists the %s descriptor twice", w.Purpose)
					break
				}
				got = &p.Files[i]
			}
		}
		switch {
		case got == nil:
			v.add(kind, "Files lists no %s descriptor", w.Purpose)
		case *got != w:
			v.add(kind, "the %s descriptor of Files is not the one the inputs and options give", w.Purpose)
		}
	}
	for _, f := range p.Files {
		switch f.Purpose {
		case PurposeSeccomp:
			if !in.SeccompEnabled() {
				v.add(ProbSeccomp, "the plan lists a seccomp descriptor and the inputs name no seccomp file")
			}
		case PurposeReport:
		default:
			v.add(ProbPassFDs, "Files lists a descriptor for a purpose nothing asks for")
		}
	}
	if len(v.out) == before && len(p.Files) != len(want) {
		v.add(ProbPassFDs, "Files lists %d descriptors, the options ask for %d", len(p.Files), len(want))
	}
	walks := wantWalks(in, f.table, opt)
	if len(p.Walks) != len(walks) {
		v.add(ProbWalk, "Walks lists %d walks, the inputs and options give %d", len(p.Walks), len(walks))
	} else {
		for i := range walks {
			if p.Walks[i] != walks[i] {
				v.add(ProbWalk, "walk %d of Walks is not the walk of %s that the inputs and options give", i+1, walks[i].Inside)
			}
		}
	}
	if i := firstDiff(p.PassFDs, passFDs(in.ProbeBinary, want, walks)); i >= 0 {
		v.add(ProbPassFDs, "the pass-through step differs from the one the inputs and options give (first difference at element %d)", i)
	}
}

// firstDiff is the index of the first element where a and b differ, or -1.
func firstDiff(a, b []string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) || i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return -1
}

// inner checks the probe command line against the options: the fixed head
// (the probe, --spec and the spec file, --report-fd with its number, --) and
// the agent argv.
func (v *verifier) inner(p *Plan, opt Options) {
	i := p.Inner
	want := probeCommand(opt)
	head := len(want) - len(opt.AgentArgv)
	if len(i) < head {
		v.add(ProbInner, "the probe command line is shorter than its fixed head of %d elements", head)
		return
	}
	for n := 0; n < head; n++ {
		if i[n] == want[n] {
			continue
		}
		if n == 3 || n == 4 {
			v.add(ProbReport, "the probe's %s and its number are not the report descriptor of the options", ProbeReportFD)
		} else {
			v.add(ProbInner, "element %d of the fixed head of the probe command line is not the expected one", n)
		}
	}
	if !equal(i[head:], opt.AgentArgv) {
		v.add(ProbAgent, "the agent argv of the plan (%d elements) is not the one of the options (%d)", len(i)-head, len(opt.AgentArgv))
	}
}

func (v *verifier) size(p *Plan) {
	if n := argvBytes(p.Argv); n > MaxArgvBytes {
		v.add(ProbSize, "the command line is %d bytes, above the cap of %d", n, MaxArgvBytes)
	}
}

// parse reads the bubblewrap arguments (without the executable) into items.
// It reports what it cannot read and returns what it could.
func (v *verifier) parse(args []string) []item {
	var items []item
	for i := 0; i < len(args); {
		a := args[i]
		if a == "--" {
			if i != len(args)-1 {
				v.add(ProbMalformed, "a -- ends the bubblewrap options before the last element")
			}
			return items
		}
		n, ok := bwrapArity[a]
		if !ok {
			n = 0
			if bad := forbiddenIn([]string{a}); bad == "" {
				v.add(ProbUnknown, "argument %d of bubblewrap is not an option the plan uses", i+1)
			}
		}
		if i+n >= len(args) {
			v.add(ProbMalformed, "%s is missing an operand", optName(a))
			return items
		}
		it := item{opt: a, args: args[i+1 : i+1+n]}
		for _, o := range it.args {
			if strings.HasPrefix(o, "--") {
				v.add(ProbOptionLike, "%s has an operand that starts with --", optName(a))
			}
		}
		items = append(items, it)
		i += 1 + n
	}
	v.add(ProbMalformed, "the bubblewrap arguments do not end in --")
	return items
}

// bwrap parses and checks the bubblewrap arguments.
func (v *verifier) bwrap(p *Plan, in profile.Inputs, opt Options, f *facts, workDir string) {
	if len(p.Bwrap) == 0 {
		return
	}
	if p.Bwrap[0] != opt.BwrapPath {
		v.add(ProbExecutable, "the bubblewrap executable is not the one of the options")
	}
	items := v.parse(p.Bwrap[1:])

	last := secFlags
	ordered := true
	var flags []string
	var setenv []profile.EnvVar
	var seccomp, chdir, remount []item
	var mounts []mount
	for _, it := range items {
		sec := sectionOf(it.opt)
		if sec < last && ordered {
			ordered = false
			v.add(ProbOrder, "%s comes after a later section (order: flags, --setenv, --seccomp, mounts, --chdir, --remount-ro)", optName(it.opt))
		}
		if sec > last {
			last = sec
		}
		switch sec {
		case secFlags:
			flags = append(flags, it.opt)
			flags = append(flags, it.args...)
		case secEnv:
			setenv = append(setenv, profile.EnvVar{Name: it.args[0], Value: it.args[1]})
		case secSeccomp:
			seccomp = append(seccomp, it)
		case secChdir:
			chdir = append(chdir, it)
		case secRemount:
			remount = append(remount, it)
		case secMount:
			m := mount{kind: mountKinds[it.opt]}
			switch it.opt {
			case "--tmpfs", "--dev", "--proc":
				m.inside = it.args[0]
			case "--symlink":
				m.target, m.inside = it.args[0], it.args[1]
			case "--ro-bind-fd", "--bind-fd":
				m.byFD, m.fdText, m.inside = true, it.args[0], it.args[1]
			default:
				m.source, m.inside = it.args[0], it.args[1]
			}
			mounts = append(mounts, m)
		}
	}
	// Before any check of a source: a mount bound from a descriptor takes its
	// source from the walk of the pass-through step that opens the descriptor.
	v.bindFDs(in, opt, mounts, parseStepWalks(p.PassFDs))

	want := f.flags
	if !equal(flags, want) {
		v.add(ProbFlags, "the flag set (%d arguments) is not the profile's %q", len(flags), strings.Join(want, " "))
	}
	v.env(p, f, setenv)
	v.seccomp(in, opt, seccomp)
	v.mounts(in, opt, f, mounts)
	v.writableSources(p, f, mounts)
	v.mountList(p, f, mounts)
	v.workDir(p, workDir, chdir)
	v.remount(remount, items)
}

func (v *verifier) env(p *Plan, f *facts, setenv []profile.EnvVar) {
	want := f.env
	if !equalEnv(p.Env, want) {
		v.add(ProbEnv, "Env of the plan is not the allowlist")
	}
	if equalEnv(setenv, want) {
		return
	}
	before := len(v.out)
	names := map[string]string{}
	for _, e := range want {
		names[e.Name] = e.Value
	}
	seen := map[string]bool{}
	for n, e := range setenv {
		// The plan's names and values are not repeated: a tampered plan may
		// hold a secret there. The position says which one.
		switch w, ok := names[e.Name]; {
		case seen[e.Name]:
			v.add(ProbEnv, "--setenv number %d sets a name that an earlier one set", n+1)
		case !ok:
			v.add(ProbEnv, "--setenv number %d sets a name that is not in the allowlist", n+1)
		case w != e.Value:
			v.add(ProbEnv, "--setenv number %d has a value other than the allowlist's", n+1)
		}
		seen[e.Name] = true
	}
	for _, e := range want {
		if !seen[e.Name] {
			v.add(ProbEnv, "%q of the allowlist is not set", e.Name)
		}
	}
	// Same names and values, wrong order: the plan promises name order.
	if len(v.out) == before {
		v.add(ProbEnv, "the --setenv options are not in name order")
	}
}

// seccomp checks the --seccomp option: absent when the inputs name no file,
// otherwise passed once with the descriptor of the options. Files is checked
// against the same descriptor by passFDs.
func (v *verifier) seccomp(in profile.Inputs, opt Options, got []item) {
	if !in.SeccompEnabled() {
		if len(got) > 0 {
			v.add(ProbSeccomp, "--seccomp is passed and the inputs name no seccomp file")
		}
		return
	}
	if len(got) != 1 {
		v.add(ProbSeccomp, "the inputs name a seccomp file and --seccomp is passed %d times", len(got))
		return
	}
	if got[0].args[0] != strconv.Itoa(opt.SeccompFD) {
		v.add(ProbSeccomp, "the --seccomp descriptor is not the seccomp descriptor of the options")
	}
}

// bindFDs pairs each mount that binds from a descriptor with the walk of the
// pass-through step that opens the descriptor, and reports every way the two
// lists do not pair one to one: a descriptor that is no number, one that two
// walks or two mounts use, a mount without a walk, a walk without a mount,
// and a bind descriptor that is the seccomp or the report descriptor of the
// options. A mount that pairs with exactly one walk gets that walk and, as its
// source, the path the walk leads to. Walks and mounts are named by their
// position: their text is the plan's.
func (v *verifier) bindFDs(in profile.Inputs, opt Options, mounts []mount, walks []*stepWalk) {
	other := map[int]string{opt.ReportFD: "report"}
	if in.SeccompEnabled() {
		other[opt.SeccompFD] = "seccomp"
	}
	byFD := map[int]*stepWalk{}
	twice := map[int]bool{}
	for n, w := range walks {
		switch {
		case w.fd == 0:
			v.add(ProbBindFD, "walk number %d of the pass-through step has a descriptor that is not a number from %d to %d", n+1, minFD, MaxBindFD)
			continue
		case byFD[w.fd] != nil:
			twice[w.fd] = true
			v.add(ProbBindFD, "walk number %d of the pass-through step uses the descriptor of an earlier walk", n+1)
			continue
		}
		if what, ok := other[w.fd]; ok {
			v.add(ProbBindFD, "walk number %d of the pass-through step uses the %s descriptor of the options", n+1, what)
		}
		byFD[w.fd] = w
	}
	for i := range mounts {
		m := &mounts[i]
		if !m.byFD {
			continue
		}
		if m.fd = fdNumber(m.fdText); m.fd == 0 {
			v.add(ProbBindFD, "mount number %d binds from a descriptor that is not a number from %d to %d", i+1, minFD, MaxBindFD)
			continue
		}
		w := byFD[m.fd]
		switch {
		case w == nil:
			v.add(ProbBindFD, "mount number %d binds from a descriptor that no walk of the pass-through step opens", i+1)
			continue
		case w.used > 0:
			v.add(ProbBindFD, "mount number %d binds from a descriptor that an earlier mount binds from", i+1)
		}
		w.used++
		if twice[m.fd] {
			// Two walks claim the number: which one the step would leave at
			// it is not the plan's to say, so the mount has no source.
			continue
		}
		m.walk, m.source = w, joinWalk(w.root, w.rel)
	}
	for n, w := range walks {
		if w.fd != 0 && byFD[w.fd] == w && w.used == 0 {
			v.add(ProbBindFD, "walk number %d of the pass-through step opens a descriptor that no mount binds from", n+1)
		}
	}
}

// expect is a row of the bind table as it must appear in the arguments.
type expect struct {
	row   profile.Bind
	index int
}

func (v *verifier) mounts(in profile.Inputs, opt Options, f *facts, got []mount) {
	table := f.table
	byInside := map[string]expect{}
	for i, b := range table {
		if b.Kind != profile.KindRemountRO {
			byInside[b.Inside] = expect{b, i}
		}
	}
	other := in.Runtime.Other()
	otherInside := profile.ConfigInside(other)
	otherHost := in.OtherConfigDir()
	wantWalk := map[string]Walk{}
	for _, w := range wantWalks(in, table, opt) {
		wantWalk[w.Inside] = w
	}

	// Paths of the plan are not repeated, only paths of the bind table: a
	// mount that is not a row is named by its position.
	seen := map[string]bool{}
	lastIndex := -1
	ordered := true
	for n, m := range got {
		e, inTable := byInside[m.inside]
		label := fmt.Sprintf("mount number %d (a path outside the table)", n+1)
		if inTable {
			label = e.row.Inside
		}
		if seen[m.inside] {
			v.add(ProbMountTwice, "%s is mounted twice", label)
		}
		seen[m.inside] = true

		if m.source != "" {
			// The lead's own mailbox and the project's own published folder
			// are sources of their own rows only: the role is the one of the
			// row at this inside path, and none for a mount outside the table.
			role := profile.RoleNone
			if inTable {
				role = e.row.SourceRole
			}
			if err := in.CheckRowSource(m.source, role); err != nil {
				v.add(ProbNever, "the source of %s is a never-mounted path, lies in or around one, lies where the rules about other state refuse it (another lead's state, the holder folder, another project's mirror or published folder, another role's mailbox, a coordinator's tree, home or login, or the lead's own mailbox or published folder as another row), or is not a clean absolute path", label)
			}
		}
		if otherInside != "" && (m.inside == otherInside || strings.HasPrefix(m.inside, otherInside+"/")) {
			v.add(ProbOtherRuntime, "%s lies in the %s config folder, which is never mounted", label, other)
		}
		if otherHost != "" && m.source != "" && (m.source == otherHost || strings.HasPrefix(m.source, otherHost+"/") || strings.HasPrefix(otherHost, m.source+"/")) {
			v.add(ProbOtherRuntime, "the source of %s is in or around the %s config folder", label, other)
		}

		if !inTable {
			v.add(ProbMountExtra, "%s is mounted and the bind table has no such row", label)
			continue
		}
		if e.index < lastIndex && ordered {
			ordered = false
			v.add(ProbOrder, "%s is mounted before a row that the table puts earlier", label)
		}
		if e.index > lastIndex {
			lastIndex = e.index
		}
		switch {
		case m.kind != e.row.Kind:
			v.add(ProbMountDiffers, "%s: the table has a %s row, the plan a %s mount", label, e.row.Kind, m.kind)
		case m.source != e.row.Source:
			v.add(ProbMountDiffers, "%s: the source is not the table's", label)
		case m.target != e.row.Target:
			v.add(ProbMountDiffers, "%s: the link target is not the table's", label)
		}
		// Rule B1: a source Ataniq supplies is bound from the descriptor the
		// pass-through step walked to, with the walk the inputs and options
		// give; a row of the host, and no other row, is bound by path.
		switch supplied := suppliedRow(e.row); {
		case supplied && !m.byFD:
			v.add(ProbBindByPath, "%s is a bind row Ataniq supplies and the plan binds it by path: it must be bound from a descriptor that the pass-through step walked to", label)
		case !supplied && m.byFD:
			v.add(ProbBindFD, "%s is bound from a descriptor, and it is not a bind row Ataniq supplies: a row of the host is bound by path", label)
		case supplied && m.walk != nil:
			w, ok := wantWalk[e.row.Inside]
			switch {
			case !ok:
				// The options were checked, so every such row has a walk.
				v.add(ProbWalk, "%s is bound from a descriptor and the options give no walk for it", label)
			case m.fd != w.FD:
				v.add(ProbWalk, "%s is bound from a descriptor other than the %d that the inputs and options give it", label, w.FD)
			case m.walk.root != w.Root || m.walk.rel != w.Rel:
				v.add(ProbWalk, "%s: the folder its walk starts from, or the path beneath it, is not the one the inputs and options give", label)
			case m.walk.dir != w.Dir:
				v.add(ProbWalk, "%s: its walk is for %s, and the row binds %s", label, kindWord(m.walk.dir), kindWord(w.Dir))
			}
		}
	}
	for _, b := range table {
		if b.Kind == profile.KindRemountRO {
			continue
		}
		// A row is present exactly when it is required, or optional and the
		// options say the host has it.
		switch want := !b.Optional || opt.HostPresent[b.Inside]; {
		case want && !seen[b.Inside]:
			v.add(ProbMountMissing, "%s (%s) is not mounted", b.Inside, b.Kind)
		case !want && seen[b.Inside]:
			v.add(ProbMountExtra, "%s is mounted and the options say the host has no such optional path", b.Inside)
		}
	}
}

// writableSources checks rule G1 on the mounts parsed from the arguments, not
// on the table: no mount has a source in or below the source of a read-write
// mount (a host folder the lead can write), and no row of Plan.Mounts says
// so. A mount that is not a row of the table is named by its position.
func (v *verifier) writableSources(p *Plan, f *facts, got []mount) {
	inTable := map[string]bool{}
	for _, b := range f.table {
		inTable[b.Inside] = true
	}
	rows := make([]srcRow, len(got))
	label := make([]string, len(got))
	for i, m := range got {
		rows[i] = srcRow{m.kind, m.inside, m.source}
		label[i] = fmt.Sprintf("mount number %d (a path outside the table)", i+1)
		if inTable[m.inside] {
			label[i] = m.inside
		}
	}
	bad, folder := writableSourceRows(rows)
	for k := range bad {
		v.add(ProbLeadWritable, "the source of %s lies in or below the source of the read-write mount %s, a folder the lead can write", label[bad[k]], label[folder[k]])
	}
	for i, m := range p.Mounts {
		if m.LeadWritable {
			v.add(ProbLeadWritable, "row %d of Mounts says its source lies in a folder the lead can write", i+1)
		}
	}
}

// mountList checks Plan.Mounts against the mounts parsed from the arguments:
// the same rows in the same order, with the descriptor a row is bound from
// (and, as its source, the path the walk of that descriptor leads to), the
// lead-writable flag worked out from the table's read-write sources and the
// optional flag from the table.
func (v *verifier) mountList(p *Plan, f *facts, got []mount) {
	writable := writableSources(f.table)
	optional := map[string]bool{}
	for _, b := range f.table {
		optional[b.Inside] = b.Optional
	}
	want := make([]Mount, 0, len(got))
	for _, m := range got {
		want = append(want, Mount{Kind: m.kind, Inside: m.inside, Source: m.source, FD: m.fd, LeadWritable: belowAny(m.source, writable), Optional: optional[m.inside]})
	}
	if len(p.Mounts) != len(want) {
		v.add(ProbMountList, "Mounts lists %d rows, the arguments mount %d", len(p.Mounts), len(want))
		return
	}
	for i := range want {
		if p.Mounts[i] != want[i] {
			v.add(ProbMountList, "row %d of Mounts is not the row the arguments mount", i+1)
		}
	}
}

func (v *verifier) workDir(p *Plan, workDir string, chdir []item) {
	if len(chdir) != 1 {
		v.add(ProbWorkDir, "--chdir is passed %d times, not once", len(chdir))
	} else if chdir[0].args[0] != workDir {
		v.add(ProbWorkDir, "--chdir is not the working folder of the options")
	}
	if p.WorkDir != workDir {
		v.add(ProbWorkDir, "the working folder of the plan is not the one of the options")
	}
}

// remount checks --remount-ro: once, for the root, and last. That it is last
// is also implied by the section order the bwrap check reports; it is kept so
// that the rule stands on its own.
func (v *verifier) remount(got []item, all []item) {
	if len(got) != 1 {
		v.add(ProbRemount, "--remount-ro is passed %d times, not once", len(got))
		return
	}
	if got[0].args[0] != "/" {
		v.add(ProbRemount, "--remount-ro is not for the root")
	}
	if all[len(all)-1].opt != "--remount-ro" {
		v.add(ProbRemount, "--remount-ro is not the last option")
	}
}

// rest checks the digest, the terminal flag and the notes.
func (v *verifier) rest(p *Plan, in profile.Inputs, opt Options, f *facts, workDir string) {
	if p.Digest != f.digest {
		v.add(ProbDigest, "the digest is not the profile's")
	}
	if p.SetControllingTerminal {
		v.add(ProbTerminal, "SetControllingTerminal is true: the probe makes the session")
	}
	v.notes(p, in, opt, f, workDir)
}

// notes compares the notes with the exact set the inputs and the options give.
func (v *verifier) notes(p *Plan, in profile.Inputs, opt Options, f *facts, workDir string) {
	want := map[string]int{}
	for _, n := range notes(in, opt, workDir, leftOut(f.table, opt)) {
		want[n]++
	}
	have := map[string]int{}
	for _, n := range p.Notes {
		have[n]++
	}
	for n, c := range want {
		if have[n] < c {
			v.add(ProbNotes, "the note %q is missing", clip(n))
		}
	}
	extra := 0
	for n, c := range have {
		if c > want[n] {
			extra += c - want[n]
		}
	}
	if extra > 0 {
		v.add(ProbNotes, "the plan carries %d notes that the inputs and options do not give", extra)
	}
}

// optName is a bubblewrap option as the plan uses it, or a placeholder for any
// other token, so that a problem never repeats text of the plan.
func optName(a string) string {
	if _, ok := bwrapArity[a]; ok {
		return a
	}
	return "an unknown option"
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalEnv(a, b []profile.EnvVar) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
