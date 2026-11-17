package profile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Version is the profile version. It is part of the digest: change it when
// the profile's data changes in a way the digest must show.
const Version = 1

const (
	digestMagic   = "ataniq-lead-sandbox-profile"
	projectToken  = "<project>"
	ownerUIDToken = "<owner-uid>"
	ownerGIDToken = "<owner-gid>"
	seccompAbsent = "none"
)

// definition is what defines the profile independent of the host and of the
// lead: no state root, role, project or host path, no owner ids and no
// environment value that comes from a host.
type definition struct {
	Version        int
	Runtime        Runtime
	RuntimeVersion string
	Identity       IdentityMode
	Flags          []string
	Binds          []Bind
	EnvNames       []string
	Seccomp        string
}

// definition builds the host-independent definition of the profile. The
// inputs must have passed Validate.
func (in Inputs) definition() definition {
	flags := flagSet("0", "0")
	if in.Identity == IdentityOwner {
		flags = flagSet(ownerUIDToken, ownerGIDToken)
	}
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
		Version:        Version,
		Runtime:        in.Runtime,
		RuntimeVersion: in.RuntimeVersion,
		Identity:       in.Identity,
		Flags:          flags,
		Binds:          t,
		EnvNames:       in.EnvNames(),
		Seccomp:        seccomp,
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
	}
	num(len(d.EnvNames))
	for _, n := range d.EnvNames {
		str(n)
	}
	str(d.Seccomp)
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
// the bind table with source roles in place of host paths, the environment
// names and the seccomp file digest (or "none"). The state root, the role, the
// project and every host path are not in it, so the same profile on two hosts,
// for two roles or for two projects gives the same digest, and a changed flag,
// bind, name or seccomp digest changes it. Environment values and the owner's
// ids are not in it either.
func (in Inputs) Digest() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	return in.definition().digest(), nil
}
