package profile

import "strconv"

// ForbiddenFlags are bubblewrap options the plan must never pass. The owner
// decision R17 allows nested sandboxes for now, so neither option that
// disables user namespaces is passed; the probe makes the session, so
// bubblewrap's own is not used either.
func ForbiddenFlags() []string {
	return []string{"--disable-userns", "--assert-userns-disabled", "--new-session"}
}

// flagSet is the bubblewrap flag set. --share-net follows --unshare-all
// because bubblewrap runs inside the network namespace pasta made.
func flagSet(uid, gid string) []string {
	return []string{
		"--die-with-parent", "--unshare-all", "--share-net", "--unshare-user",
		"--uid", uid, "--gid", gid,
		"--cap-drop", "ALL", "--clearenv",
	}
}

// Flags validates the inputs and returns the bubblewrap flag set: UID and GID
// 0 for IdentityRoot, the owner's ids for IdentityOwner. The optional
// --seccomp option is not in it: the plan adds it, with a descriptor, only
// when SeccompEnabled says a reviewed BPF file is named.
func (in Inputs) Flags() ([]string, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.Identity == IdentityOwner {
		return flagSet(strconv.FormatUint(uint64(in.OwnerUID), 10), strconv.FormatUint(uint64(in.OwnerGID), 10)), nil
	}
	return flagSet("0", "0"), nil
}
