package profile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"ataniq/internal/leadsandbox/sandboxnet"
)

// Version is the profile version. It is part of the digest: change it when
// the profile's data changes in a way the digest must show.
//
//	1  first version
//	2  the digest carries the values of the variables the profile defines,
//	   the digest of the managed-settings file, the pasta arguments and the
//	   optional flag of a row, and lists the extra placeholders sorted
//	3  the three read-only .git rows take their sources from the Ataniq-owned
//	   git metadata folder (source role git-meta, not tree-git): no source of
//	   the table lies in a folder the lead can write
//	4  the layout of the lead-hosting design, revision 4: every table has the
//	   rows /ataniq/requests, /ataniq/mailbox, /ataniq/mail and /coord
const Version = 4

const (
	digestMagic   = "ataniq-lead-sandbox-profile"
	projectToken  = "<project>"
	ownerUIDToken = "<owner-uid>"
	ownerGIDToken = "<owner-gid>"
	seccompAbsent = "none"
)

// definition is what defines the profile independent of the host and of the
// lead: no state root, role, project or host path, no owner ids and no
// environment value that comes from a host or a lead.
type definition struct {
	Version        int
	Runtime        Runtime
	RuntimeVersion string
	Identity       IdentityMode
	Flags          []string
	Binds          []Bind
	// EnvNames is every name the profile defines, sorted. EnvValues holds
	// the names and values of the variables whose value the profile defines
	// (not the host-terminal ones, the lead identity and, in owner mode, the
	// user name), sorted by name.
	EnvNames  []string
	EnvValues []EnvVar
	Seccomp   string
	// ManagedSettings is the SHA-256 of the managed-settings file the
	// caller gave. Network is the argument vector for pasta.
	ManagedSettings string
	Network         []string
}

// definition builds the host-independent definition of the profile. The
// inputs must have passed Validate.
func (in Inputs) definition() definition {
	flags := flagSet("0", "0")
	if in.Identity == IdentityOwner {
		flags = flagSet(ownerUIDToken, ownerGIDToken)
	}
	// The placeholders are sorted by target, so the digest does not depend on
	// the order the caller lists them in (they cannot overlap).
	in.ExtraPlaceholders = append([]Placeholder(nil), in.ExtraPlaceholders...)
	sort.Slice(in.ExtraPlaceholders, func(i, j int) bool { return in.ExtraPlaceholders[i].Rel < in.ExtraPlaceholders[j].Rel })
	t := in.table(TreeInside(projectToken), MirrorInside(projectToken))
	// A host path is replaced by its source role.
	for i := range t {
		t[i].Source = ""
	}
	seccomp := seccompAbsent
	if in.SeccompEnabled() {
		seccomp = in.SeccompSHA256
	}
	return definition{
		Version:         Version,
		Runtime:         in.Runtime,
		RuntimeVersion:  in.RuntimeVersion,
		Identity:        in.Identity,
		Flags:           flags,
		Binds:           t,
		EnvNames:        in.EnvNames(),
		EnvValues:       in.profileEnvValues(),
		Seccomp:         seccomp,
		ManagedSettings: in.ManagedSettingsSHA256,
		Network:         sandboxnet.PastaArgs(),
	}
}

// encode is the canonical encoding: a magic string, then every field in a
// fixed order, each string as a length (uvarint) and its bytes, each list as a
// count and its items, each flag as one byte.
func (d definition) encode() []byte {
	var b []byte
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	num := func(n int) { b = binary.AppendUvarint(b, uint64(n)) }
	flag := func(v bool) {
		if v {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	str(digestMagic)
	num(d.Version)
	str(string(d.Runtime))
	str(d.RuntimeVersion)
	str(string(d.Identity))
	num(len(d.Flags))
	for _, f := range d.Flags {
		str(f)
	}
	num(len(d.Binds))
	for _, bind := range d.Binds {
		str(string(bind.Kind))
		str(bind.Inside)
		str(bind.SourceRole)
		str(bind.Target)
		flag(bind.IsDir)
		flag(bind.LeadWritable)
		str(bind.Within)
		flag(bind.Optional)
	}
	num(len(d.EnvNames))
	for _, n := range d.EnvNames {
		str(n)
	}
	num(len(d.EnvValues))
	for _, e := range d.EnvValues {
		str(e.Name)
		str(e.Value)
	}
	str(d.Seccomp)
	str(d.ManagedSettings)
	num(len(d.Network))
	for _, a := range d.Network {
		str(a)
	}
	return b
}

// digest is the SHA-256 of the encoding, in lower-case hex.
func (d definition) digest() string {
	sum := sha256.Sum256(d.encode())
	return hex.EncodeToString(sum[:])
}

// Digest validates the inputs and returns the SHA-256 (lower-case hex) of a
// canonical, versioned encoding of what defines the profile: the profile
// version, the runtime, the pinned version, the identity mode, the flag set,
// the bind table with source roles in place of host paths, every environment
// name, the value of every variable whose value the profile defines (the
// fixed values, PATH, SHELL, the config variable, the git and Go variables,
// every extra variable), the seccomp file digest (or "none"), the digest of
// the managed-settings file, and the pasta arguments of sandboxnet.PastaArgs.
// The state
// root, the role, the project and every host path are not in it, nor are the
// values the host terminal or the lead gives (TERM, COLORTERM, LANG, TZ,
// QARGI_LEAD, and USER and LOGNAME in owner mode, which are in it by name
// only) or the owner's ids, so the same profile on two hosts, for two roles
// or for two projects gives the same digest, and a changed flag, bind, name,
// profile-defined value or seccomp digest changes it.
func (in Inputs) Digest() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	return in.definition().digest(), nil
}
