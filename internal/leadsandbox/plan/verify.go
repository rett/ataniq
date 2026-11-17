package plan

import (
	"fmt"
	"path"
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
	ProbShape        ProblemKind = "shape"            // a plan with missing or inconsistent parts
	ProbExecutable   ProblemKind = "executable"       // pasta or bubblewrap path is not absolute
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
	ProbSeccomp      ProblemKind = "seccomp"          // --seccomp, its descriptor or Files
	ProbMountMissing ProblemKind = "mount-missing"    // a table row is not mounted
	ProbMountExtra   ProblemKind = "mount-unexpected" // a mount that is not in the table
	ProbMountDiffers ProblemKind = "mount-differs"    // a row with another kind, source or target
	ProbMountTwice   ProblemKind = "mount-twice"      // one inside path mounted twice
	ProbNever        ProblemKind = "never-mounted"    // a source in or around a never-mounted path
	ProbOtherRuntime ProblemKind = "other-runtime"    // the other runtime's config folder
	ProbWorkDir      ProblemKind = "workdir"          // --chdir missing, outside the tree and home
	ProbRemount      ProblemKind = "remount-ro"       // --remount-ro / missing or not last
	ProbInner        ProblemKind = "inner"            // the probe command line
	ProbDigest       ProblemKind = "digest"           // not the profile digest
	ProbNotes        ProblemKind = "notes"            // a required note is missing
	ProbTerminal     ProblemKind = "terminal"         // SetControllingTerminal is true
	ProbSize         ProblemKind = "size"             // the command line is above the cap
)

// Problem is one thing wrong with a plan. Detail never holds the value of an
// environment variable or an agent argument.
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
	case "--tmpfs", "--dev", "--proc", "--ro-bind", "--bind", "--symlink":
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
}

// Verify checks a finished plan against the profile inputs, without trusting
// the code that built it: it parses the bubblewrap arguments back into flags,
// environment and mounts and compares them with what the profile gives. It
// returns no problem for a plan that agrees with the profile. An optional row
// may be absent; a row that is not optional may not.
//
// The holder calls it before every start. It checks the shape of the argument
// list; it does not check that a source exists, nor what bubblewrap or pasta
// do with the arguments.
func Verify(p *Plan, in profile.Inputs) []Problem {
	v := &verifier{}
	if err := in.Validate(); err != nil {
		v.add(ProbInputs, "%s", err)
		return v.out
	}
	if p == nil {
		v.add(ProbShape, "there is no plan")
		return v.out
	}
	v.shape(p)
	v.pasta(p)
	v.passFDs(p, in)
	v.inner(p, in)
	v.size(p)
	if bad := forbiddenIn(p.Argv); bad != "" {
		v.add(ProbForbidden, "%s appears in the command line", bad)
	}
	if bad := forbiddenIn(join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)); bad != "" && forbiddenIn(p.Argv) == "" {
		v.add(ProbForbidden, "%s appears in a part of the plan", bad)
	}
	v.bwrap(p, in)
	v.rest(p, in)
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

func (v *verifier) pasta(p *Plan) {
	if len(p.Pasta) == 0 {
		return
	}
	if !path.IsAbs(p.Pasta[0]) {
		v.add(ProbExecutable, "the pasta executable is not an absolute path")
	}
	want := sandboxnet.PastaArgs()
	got := p.Pasta[1:]
	if !equal(got, want) {
		v.add(ProbPasta, "the pasta arguments are not the %d of sandboxnet.PastaArgs", len(want))
	}
}

// passFDs checks the pass-through step: the probe binary of the inputs, the
// pass-fds mode, the --read and --write operands that Files lists, reads
// before writes, and the terminator.
func (v *verifier) passFDs(p *Plan, in profile.Inputs) {
	s := p.PassFDs
	if len(s) == 0 {
		return
	}
	if s[0] != in.ProbeBinary {
		v.add(ProbPassFDs, "the pass-through step does not run the probe binary of the inputs")
	}
	if len(s) < 2 || s[1] != PassFDsMode {
		v.add(ProbPassFDs, "the pass-through step is not in the %s mode", PassFDsMode)
		return
	}
	if s[len(s)-1] != "--" {
		v.add(ProbPassFDs, "the pass-through step does not end in --")
		return
	}
	ops := s[2 : len(s)-1]
	if len(ops)%2 != 0 {
		v.add(ProbPassFDs, "an option of the pass-through step is missing its operand")
		return
	}
	var got []File
	wrote := false
	for i := 0; i < len(ops); i += 2 {
		var mode FileMode
		switch ops[i] {
		case PassFDsRead:
			mode = FileRead
			if wrote {
				v.add(ProbPassFDs, "a %s follows a %s", PassFDsRead, PassFDsWrite)
			}
		case PassFDsWrite:
			mode = FileWrite
			wrote = true
		default:
			v.add(ProbPassFDs, "the pass-through step has an option that is neither %s nor %s", PassFDsRead, PassFDsWrite)
			return
		}
		num, path, ok := strings.Cut(ops[i+1], "=")
		fd, err := strconv.Atoi(num)
		if !ok || err != nil || strconv.Itoa(fd) != num {
			v.add(ProbPassFDs, "an operand of the pass-through step is not FD=PATH")
			return
		}
		got = append(got, File{FD: fd, Path: path, Mode: mode})
	}
	if len(got) != len(p.Files) {
		v.add(ProbPassFDs, "the pass-through step opens %d descriptors, Files lists %d", len(got), len(p.Files))
		return
	}
	for i, g := range got {
		f := p.Files[i]
		if g.FD != f.FD || g.Path != f.Path || g.Mode != f.Mode {
			v.add(ProbPassFDs, "descriptor %d of the pass-through step is not the one Files lists", i)
		}
	}
	seen := map[int]bool{}
	for _, f := range p.Files {
		if f.FD < MinReportFD || f.FD > MaxReportFD {
			v.add(ProbPassFDs, "a descriptor of the plan is outside %d to %d", MinReportFD, MaxReportFD)
		}
		if seen[f.FD] {
			v.add(ProbPassFDs, "a descriptor number is used twice")
		}
		seen[f.FD] = true
	}
}

func (v *verifier) inner(p *Plan, in profile.Inputs) {
	i := p.Inner
	if len(i) < 7 {
		v.add(ProbInner, "the probe command line needs the probe, --spec, the spec file, --report-fd, its number, -- and the agent")
		return
	}
	head := []string{profile.ProbeInside, "--spec", profile.SpecFileInside, ProbeReportFD}
	if !equal(i[:4], head) || i[5] != "--" {
		v.add(ProbInner, "the probe command line does not have the form %s FD -- AGENT...", strings.Join(head, " "))
	}
	if fd, err := strconv.Atoi(i[4]); err != nil || strconv.Itoa(fd) != i[4] {
		v.add(ProbReport, "the probe's --report-fd is not a plain number")
	} else if w := p.reportFile(); w == nil {
		v.add(ProbReport, "the plan lists no report channel")
	} else if w.FD != fd {
		v.add(ProbReport, "the probe's --report-fd is not the descriptor the pass-through step opens for the report")
	}
	if !path.IsAbs(i[6]) {
		v.add(ProbInner, "the agent executable is not an absolute path")
	}
	for n, a := range i {
		if strings.IndexByte(a, 0) >= 0 {
			v.add(ProbInner, "element %d of the probe command line holds a NUL", n)
		}
	}
}

// reportFile is the one write-only descriptor of Files, or nil.
func (p *Plan) reportFile() *File {
	var w *File
	for i := range p.Files {
		if p.Files[i].Purpose == PurposeReport && p.Files[i].Mode == FileWrite {
			if w != nil {
				return nil
			}
			w = &p.Files[i]
		}
	}
	return w
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
				v.add(ProbUnknown, "the bubblewrap option %q is not one the plan uses", clip(a))
			}
		}
		if i+n >= len(args) {
			v.add(ProbMalformed, "%s is missing an operand", a)
			return items
		}
		it := item{opt: a, args: args[i+1 : i+1+n]}
		for _, o := range it.args {
			if strings.HasPrefix(o, "--") {
				v.add(ProbOptionLike, "%s has an operand that starts with --", a)
			}
		}
		items = append(items, it)
		i += 1 + n
	}
	v.add(ProbMalformed, "the bubblewrap arguments do not end in --")
	return items
}

// bwrap parses and checks the bubblewrap arguments.
func (v *verifier) bwrap(p *Plan, in profile.Inputs) {
	if len(p.Bwrap) == 0 {
		return
	}
	if !path.IsAbs(p.Bwrap[0]) {
		v.add(ProbExecutable, "the bubblewrap executable is not an absolute path")
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
			v.add(ProbOrder, "%s comes after a later section (order: flags, --setenv, --seccomp, mounts, --chdir, --remount-ro)", it.opt)
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
			default:
				m.source, m.inside = it.args[0], it.args[1]
			}
			mounts = append(mounts, m)
		}
	}

	want, err := in.Flags()
	if err == nil && !equal(flags, want) {
		v.add(ProbFlags, "the flag set is %q, the profile's is %q", strings.Join(flags, " "), strings.Join(want, " "))
	}
	v.env(p, in, setenv)
	v.seccomp(p, in, seccomp)
	v.mounts(in, mounts)
	v.workDir(p, in, chdir)
	v.remount(remount, items)
}

func (v *verifier) env(p *Plan, in profile.Inputs, setenv []profile.EnvVar) {
	want, err := in.Environment()
	if err != nil {
		return
	}
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
	for _, e := range setenv {
		switch w, ok := names[e.Name]; {
		case seen[e.Name]:
			v.add(ProbEnv, "%q is set twice", clip(e.Name))
		case !ok:
			v.add(ProbEnv, "%q is set and is not in the allowlist", clip(e.Name))
		case w != e.Value:
			v.add(ProbEnv, "%q has a value other than the allowlist's", clip(e.Name))
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

func (v *verifier) seccomp(p *Plan, in profile.Inputs, got []item) {
	var secFiles []File
	for _, f := range p.Files {
		if f.Purpose == PurposeSeccomp || f.Mode == FileRead {
			secFiles = append(secFiles, f)
		}
	}
	if !in.SeccompEnabled() {
		if len(got) > 0 {
			v.add(ProbSeccomp, "--seccomp is passed and the inputs name no seccomp file")
		}
		if len(secFiles) > 0 {
			v.add(ProbSeccomp, "the plan lists a seccomp descriptor and the inputs name no seccomp file")
		}
		return
	}
	if len(got) != 1 {
		v.add(ProbSeccomp, "the inputs name a seccomp file and --seccomp is passed %d times", len(got))
		return
	}
	fd, err := strconv.Atoi(got[0].args[0])
	switch {
	case err != nil || strconv.Itoa(fd) != got[0].args[0]:
		v.add(ProbSeccomp, "the --seccomp descriptor is not a plain number")
		return
	case fd < MinSeccompFD || fd > MaxSeccompFD:
		v.add(ProbSeccomp, "the --seccomp descriptor %d is outside %d to %d", fd, MinSeccompFD, MaxSeccompFD)
	}
	if len(secFiles) != 1 {
		v.add(ProbSeccomp, "the plan lists %d read descriptors, not the one seccomp file", len(secFiles))
		return
	}
	f := secFiles[0]
	if f.FD != fd {
		v.add(ProbSeccomp, "the descriptor the pass-through step opens (%d) is not the one --seccomp names (%d)", f.FD, fd)
	}
	if f.Path != in.SeccompFile || f.SHA256 != in.SeccompSHA256 || f.Mode != FileRead || f.Purpose != PurposeSeccomp {
		v.add(ProbSeccomp, "the descriptor does not carry the seccomp file and digest of the inputs, read-only")
	}
}

// expect is a row of the bind table as it must appear in the arguments.
type expect struct {
	row   profile.Bind
	index int
}

func (v *verifier) mounts(in profile.Inputs, got []mount) {
	table, err := in.Binds()
	if err != nil {
		return
	}
	byInside := map[string]expect{}
	for i, b := range table {
		if b.Kind != profile.KindRemountRO {
			byInside[b.Inside] = expect{b, i}
		}
	}
	other := in.Runtime.Other()
	otherInside := profile.ConfigInside(other)
	otherHost := in.OtherConfigDir()

	seen := map[string]bool{}
	lastIndex := -1
	ordered := true
	for _, m := range got {
		if seen[m.inside] {
			v.add(ProbMountTwice, "%s is mounted twice", m.inside)
		}
		seen[m.inside] = true

		if m.source != "" {
			if err := in.CheckSource(m.source); err != nil {
				v.add(ProbNever, "the source of %s: %s", m.inside, reason(err))
			}
		}
		if otherInside != "" && (m.inside == otherInside || strings.HasPrefix(m.inside, otherInside+"/")) {
			v.add(ProbOtherRuntime, "%s is the %s config folder, which is never mounted", m.inside, other)
		}
		if otherHost != "" && m.source != "" && (m.source == otherHost || strings.HasPrefix(m.source, otherHost+"/") || strings.HasPrefix(otherHost, m.source+"/")) {
			v.add(ProbOtherRuntime, "the source of %s is in or around the %s config folder", m.inside, other)
		}

		e, ok := byInside[m.inside]
		if !ok {
			v.add(ProbMountExtra, "%s is mounted and the bind table has no such row", m.inside)
			continue
		}
		if e.index < lastIndex && ordered {
			ordered = false
			v.add(ProbOrder, "%s is mounted before a row that the table puts earlier", m.inside)
		}
		if e.index > lastIndex {
			lastIndex = e.index
		}
		if m.kind != e.row.Kind || m.source != e.row.Source || m.target != e.row.Target {
			v.add(ProbMountDiffers, "%s: the table has a %s row from %q (link %q), the plan a %s mount from %q (link %q)",
				m.inside, e.row.Kind, e.row.Source, e.row.Target, m.kind, m.source, m.target)
		}
	}
	for _, b := range table {
		if b.Kind == profile.KindRemountRO || seen[b.Inside] || b.Optional {
			continue
		}
		v.add(ProbMountMissing, "%s (%s) is not mounted", b.Inside, b.Kind)
	}
}

func (v *verifier) workDir(p *Plan, in profile.Inputs, chdir []item) {
	if len(chdir) != 1 {
		v.add(ProbWorkDir, "--chdir is passed %d times, not once", len(chdir))
		return
	}
	dir := chdir[0].args[0]
	tree := profile.TreeInside(in.Project)
	if !path.IsAbs(dir) || path.Clean(dir) != dir {
		v.add(ProbWorkDir, "--chdir is not a clean absolute path")
		return
	}
	if !within(dir, tree) && !within(dir, profile.HomeInside) {
		v.add(ProbWorkDir, "--chdir %s is outside the tree and the home", dir)
	}
	if dir != p.WorkDir {
		v.add(ProbWorkDir, "--chdir %s is not the working folder of the plan", dir)
	}
}

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

// rest checks the digest, the notes and the terminal flag.
func (v *verifier) rest(p *Plan, in profile.Inputs) {
	if d, err := in.Digest(); err == nil && p.Digest != d {
		v.add(ProbDigest, "the digest is not the profile's")
	}
	if p.SetControllingTerminal {
		v.add(ProbTerminal, "SetControllingTerminal is true: the probe makes the session")
	}
	have := map[string]bool{}
	for _, n := range p.Notes {
		have[n] = true
	}
	for _, n := range []string{NoteShareNet, NoteNestedUserns, NoteSession} {
		if !have[n] {
			v.add(ProbNotes, "the note %q is missing", clip(n))
		}
	}
	if w := p.reportFile(); w != nil && !have[noteReport(w.FD)] {
		v.add(ProbNotes, "the note about the report channel is missing")
	}
	named := false
	for _, n := range p.Notes {
		if strings.HasPrefix(n, "seccomp file named by the caller") {
			named = true
		}
	}
	switch {
	case in.SeccompEnabled() && !named:
		v.add(ProbNotes, "the plan names a seccomp file and its notes do not say so")
	case !in.SeccompEnabled() && !have[NoteNoSeccomp]:
		v.add(ProbNotes, "the plan has no seccomp file and its notes do not say %q", NoteNoSeccomp)
	}
}

func reason(err error) string {
	if r, ok := err.(*profile.Refusal); ok {
		return r.Reason
	}
	return err.Error()
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
