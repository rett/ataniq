package profile

import "sort"

// Kind is the kind of one row of the bind table.
type Kind string

// The kinds of rows, as bubblewrap makes them.
const (
	KindTmpfs     Kind = "tmpfs"
	KindDev       Kind = "dev"
	KindProc      Kind = "proc"
	KindRoBind    Kind = "ro-bind"
	KindRwBind    Kind = "rw-bind"
	KindSymlink   Kind = "symlink"
	KindRemountRO Kind = "remount-ro"
)

const rootInsidePath = "/"

// Source roles name where a row's source comes from, in place of a host path.
// The digest uses roles, so two hosts give the same digest.
const (
	RoleNone            = ""
	RoleHost            = "host" // the host path equals the inside path
	RoleResolv          = "resolv-conf"
	RoleProbe           = "probe"
	RoleSpec            = "spec"
	RoleAgent           = "agent"
	RoleManagedSettings = "managed-settings"
	RoleHome            = "home"
	RoleConfig          = "config"
	RoleSkills          = "skills"
	RoleInstructions    = "instructions"
	RoleGitIdentity     = "git-identity"
	RoleTree            = "tree"
	RoleGitMeta         = "git-meta"
	RoleMirror          = "mirror"
	RoleQargi           = "qargi"
	RoleRecords         = "qargi-records"
	RoleAssignment      = "assignment"
	RoleProjection      = "projection"
	RoleExtra           = "extra-placeholder"
)

// Bind is one row of the bind table (the lead-hosting design, section 3.3).
type Bind struct {
	Kind Kind
	// Inside is the target path inside the sandbox.
	Inside string
	// Source is the host path (read-only and read-write binds only). It never
	// lies in or below a host folder the lead can write (the source of a
	// read-write row): bubblewrap resolves a source on the host, outside
	// every sandbox, and follows a symbolic link there, so a source the lead
	// could replace with a link would bring any folder of the owner into the
	// sandbox. ValidateTable refuses such a row.
	Source string
	// SourceRole says where Source comes from (RoleHost, RoleHome, ...).
	SourceRole string
	// Target is the link target (symlink rows only).
	Target string
	// IsDir says the target is a folder; else a file. It tells the code that
	// makes mount points what to make.
	IsDir bool
	// LeadWritable says the TARGET lies inside a folder the lead can write,
	// so its mount point is made by Ataniq before the lead's first run and
	// checked on every start, never made by bubblewrap at start. It says
	// nothing about the source, which is never in such a folder.
	LeadWritable bool
	// Within is the inside path of the lead-writable read-write bind that
	// holds the target ("" when LeadWritable is false).
	Within string
	// Optional says the row is applied only if the host has it: for a
	// read-only bind of a host path (RoleHost), only if the source exists;
	// for a symlink, only if the host has a path of the same name as Inside.
	// It is set for exactly the host rows that do not exist on every
	// distribution (see the table), never for a source Ataniq makes. The plan
	// package decides how to emit it (for bubblewrap, the row is emitted with
	// --ro-bind only when the caller says the source exists, never with
	// --ro-bind-try). It is part of the digest.
	Optional bool
}

// Binds validates the inputs and returns the bind table of the epoch's
// runtime. The other runtime's config folder never appears in it.
func (in Inputs) Binds() ([]Bind, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return in.table(TreeInside(in.Project), MirrorInside(in.Project)), nil
}

// table builds the rows in an order valid for bubblewrap. treeInside and
// mirrorInside carry the project, so the digest can build a table that does
// not. The inputs must have passed check.
func (in Inputs) table(treeInside, mirrorInside string) []Bind {
	rt := in.Runtime
	l := in.Layout()
	cfg, _ := l.ConfigDir(rt)
	host := func(p string, dir bool) Bind {
		return Bind{Kind: KindRoBind, Inside: p, Source: p, SourceRole: RoleHost, IsDir: dir}
	}
	// hostTry is a host row that does not exist on every distribution.
	hostTry := func(p string, dir bool) Bind {
		b := host(p, dir)
		b.Optional = true
		return b
	}
	ro := func(inside, src, role string, dir bool) Bind {
		return Bind{Kind: KindRoBind, Inside: inside, Source: src, SourceRole: role, IsDir: dir}
	}
	rw := func(inside, src, role string) Bind {
		return Bind{Kind: KindRwBind, Inside: inside, Source: src, SourceRole: role, IsDir: true}
	}

	t := []Bind{{Kind: KindTmpfs, Inside: rootInsidePath, IsDir: true}}
	t = append(t, host("/usr", true))
	// /lib64 and /sbin are links to /usr only on some distributions.
	for _, l := range []struct {
		inside, target string
		optional       bool
	}{{"/bin", "usr/bin", false}, {"/lib", "usr/lib", false}, {"/lib64", "usr/lib64", true}, {"/sbin", "usr/sbin", true}} {
		t = append(t, Bind{Kind: KindSymlink, Inside: l.inside, Target: l.target, Optional: l.optional})
	}
	t = append(t,
		hostTry("/etc/ssl", true),
		hostTry("/etc/ca-certificates", true),
		host("/etc/hosts", false),
		hostTry("/etc/nsswitch.conf", false),
		host("/etc/passwd", false),
		host("/etc/group", false),
		hostTry("/etc/localtime", false),
		ro(ResolvConfInside, in.ResolvConf, RoleResolv, false),
		Bind{Kind: KindDev, Inside: "/dev", IsDir: true},
		Bind{Kind: KindProc, Inside: "/proc", IsDir: true},
		Bind{Kind: KindTmpfs, Inside: TmpInside, IsDir: true},
		ro(ProbeInside, in.ProbeBinary, RoleProbe, false),
		ro(SpecDirInside, in.SpecDir, RoleSpec, true),
		ro(AgentInside, in.AgentInstall, RoleAgent, true),
		ro(in.ManagedInside(), in.ManagedSettings, RoleManagedSettings, false),
		rw(HomeInside, l.Home(), RoleHome),
		rw(ConfigInside(rt), cfg, RoleConfig),
		ro(SkillsInside(rt), in.LeadSkills, RoleSkills, true),
		ro(InstructionInside(rt), in.InstructionFile, RoleInstructions, false),
		ro(GitIdentityInside, in.GitIdentityFile, RoleGitIdentity, false),
		rw(treeInside, l.Tree(), RoleTree),
	)
	// The three protected git paths keep their targets in the tree, but their
	// sources are Ataniq's copies, in a folder the lead cannot write.
	for _, rel := range GitReadOnlyRel() {
		t = append(t, ro(treeInside+"/"+rel, GitMetaSource(in.GitMeta, rel), RoleGitMeta, rel != ".git/config"))
	}
	for _, p := range in.ExtraPlaceholders {
		t = append(t, ro(treeInside+"/"+p.Rel, p.Source, RoleExtra, p.Dir))
	}
	t = append(t,
		ro(mirrorInside, l.Mirror(), RoleMirror, true),
		ro(QargiInside, in.QargiInstall, RoleQargi, true),
	)
	if in.QargiRecords != "" {
		t = append(t, rw(in.QargiRecordsInside, in.QargiRecords, RoleRecords))
	}
	t = append(t,
		ro(AssignmentInside, in.AssignmentDir, RoleAssignment, true),
		ro(ProjectionInside, in.ProjectionDir, RoleProjection, true),
		Bind{Kind: KindRemountRO, Inside: rootInsidePath, IsDir: true},
	)
	markWritable(t)
	return t
}

// markWritable sets LeadWritable and Within on every row whose target lies
// below a read-write bind of a folder the lead writes.
func markWritable(t []Bind) {
	for i := range t {
		t[i].LeadWritable, t[i].Within = false, ""
		if t[i].Kind == KindTmpfs || t[i].Kind == KindRemountRO {
			continue
		}
		for j := range t {
			if t[j].Kind == KindRwBind && j != i && below(t[i].Inside, t[j].Inside) &&
				len(t[j].Inside) > len(t[i].Within) {
				t[i].LeadWritable, t[i].Within = true, t[j].Inside
			}
		}
	}
}

// ValidateTable checks that a bind table is valid for bubblewrap: it starts
// with the root tmpfs and ends with the root remount, every row is well
// formed, no inside path appears twice, a parent comes before what is bound
// over it, a row lies only below a read-write bind or a tmpfs (never below a
// read-only bind, /dev, /proc or a symlink), LeadWritable and Within say
// what they must, and no row has a source that is, or lies below, the source
// of a read-write row, that is, in a host folder the lead can write.
func ValidateTable(t []Bind) error {
	n := len(t)
	if n < 2 || t[0].Kind != KindTmpfs || t[0].Inside != rootInsidePath {
		return refuse("bind table", "must start with the root tmpfs")
	}
	if t[n-1].Kind != KindRemountRO || t[n-1].Inside != rootInsidePath {
		return refuse("bind table", "must end with the read-only remount of the root")
	}
	seen := map[string]int{}
	for i, b := range t {
		field := "bind " + b.Inside
		if err := checkRow(field, i, n, b); err != nil {
			return err
		}
		if i == 0 || i == n-1 {
			continue
		}
		if j, ok := seen[b.Inside]; ok {
			return refuse(field, "the inside path is already used by bind %d", j)
		}
		seen[b.Inside] = i
	}
	for i := 1; i < n-1; i++ {
		for j := 1; j < n-1; j++ {
			if i == j || !below(t[i].Inside, t[j].Inside) {
				continue
			}
			field := "bind " + t[i].Inside
			if j > i {
				return refuse(field, "is bound before its parent %s", t[j].Inside)
			}
			if t[j].Kind != KindRwBind && t[j].Kind != KindTmpfs {
				return refuse(field, "lies below %s, a %s row", t[j].Inside, t[j].Kind)
			}
		}
	}
	want := append([]Bind(nil), t...)
	markWritable(want)
	for i := range t {
		if t[i].LeadWritable != want[i].LeadWritable || t[i].Within != want[i].Within {
			return refuse("bind "+t[i].Inside, "the lead-writable flag does not match the table")
		}
	}
	return checkSourcesNotWritable(t)
}

// checkSourcesNotWritable refuses a row whose source is, or lies below, the
// source of a read-write row: a host folder the lead can write. bubblewrap
// follows a symbolic link in a bind source, resolved on the host outside every
// sandbox, so a source the lead can replace is a path out of the sandbox. A
// folder the lead writes is bound whole, from an Ataniq-owned parent, or its
// content is copied in. The read-write row's own source is not a problem: the
// lead cannot replace the folder it is bound to.
func checkSourcesNotWritable(t []Bind) error {
	for i, b := range t {
		if b.Source == "" {
			continue
		}
		for j, w := range t {
			if j == i || w.Kind != KindRwBind || w.Source == "" {
				continue
			}
			if b.Source == w.Source || below(b.Source, w.Source) {
				return refuse("bind "+b.Inside, "the source %s overlaps the lead-writable folder %s (%s, bound at %s): no source may lie in a folder the lead can write", b.Source, w.Source, w.SourceRole, w.Inside)
			}
		}
	}
	return nil
}

func checkRow(field string, i, n int, b Bind) error {
	if err := CheckPath(field, b.Inside); err != nil {
		return err
	}
	if b.Optional && !(b.Kind == KindSymlink || b.Kind == KindRoBind && b.SourceRole == RoleHost) {
		return refuse(field, "only a host read-only bind or a symlink can be optional")
	}
	switch b.Kind {
	case KindTmpfs, KindDev, KindProc, KindRemountRO:
		if b.Source != "" || b.Target != "" || b.SourceRole != RoleNone {
			return refuse(field, "a %s row has no source or link target", b.Kind)
		}
		if b.Kind == KindRemountRO && i != n-1 {
			return refuse(field, "the read-only remount belongs at the end")
		}
		if b.Inside == rootInsidePath && b.Kind != KindRemountRO && i != 0 {
			return refuse(field, "only the first row mounts the root")
		}
	case KindRoBind, KindRwBind:
		if b.Inside == rootInsidePath {
			return refuse(field, "binds over the root")
		}
		if err := CheckPath(field+" source", b.Source); err != nil {
			return err
		}
		if b.Target != "" || b.SourceRole == RoleNone {
			return refuse(field, "a bind has a source role and no link target")
		}
	case KindSymlink:
		if b.Source != "" || b.Target == "" || hasControl(b.Target) || len(b.Target) > maxPathLen {
			return refuse(field, "a symlink has a link target and no source")
		}
	default:
		return refuse(field, "unknown kind %q", b.Kind)
	}
	return nil
}

// HostMountPoint maps a lead-writable row to the host path where its mount
// point lies: the source of the read-write bind that holds it, plus the rest
// of the inside path. It reports false for a row that is not lead-writable.
func HostMountPoint(t []Bind, i int) (string, bool) {
	if i < 0 || i >= len(t) || !t[i].LeadWritable {
		return "", false
	}
	for _, b := range t {
		if b.Kind == KindRwBind && b.Inside == t[i].Within {
			return b.Source + t[i].Inside[len(b.Inside):], true
		}
	}
	return "", false
}

// checkTable builds the table and runs every table-level check.
func (in Inputs) checkTable() error {
	t := in.table(TreeInside(in.Project), MirrorInside(in.Project))
	if err := ValidateTable(t); err != nil {
		return err
	}
	never := in.neverMounted()
	var rwSrc []Bind
	for _, b := range t {
		if b.Source == "" {
			continue
		}
		if err := CheckNeverMounted(b.Source, never); err != nil {
			return refuse("bind "+b.Inside, "%s", reasonOf(err))
		}
		// The profile knows the state root: a source in another lead's
		// state (its login, its home, its tree) or in another project's
		// mirror is never right, whatever the caller lists as never-mounted.
		if why := in.otherStateReason(b.Source); why != "" {
			return refuse("bind "+b.Inside, "the source %s %s", b.Source, why)
		}
		if b.Kind == KindRwBind {
			rwSrc = append(rwSrc, b)
		}
	}
	// A source that Ataniq owns and binds read-only must not be reachable
	// through a folder the lead can write, and must not contain one; two
	// writable folders must not overlap. (ValidateTable has refused a source
	// in or below a writable folder; this adds the sources above one.)
	for _, b := range t {
		if b.Source == "" {
			continue
		}
		for _, w := range rwSrc {
			if w.Inside == b.Inside {
				continue
			}
			if related(b.Source, w.Source) {
				return refuse("bind "+b.Inside, "the source %s overlaps the lead-writable folder %s (%s)", b.Source, w.Source, w.SourceRole)
			}
		}
	}
	return nil
}

func reasonOf(err error) string {
	if r, ok := err.(*Refusal); ok {
		return r.Reason
	}
	return err.Error()
}

// sortedCopy returns a sorted copy of s.
func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}
