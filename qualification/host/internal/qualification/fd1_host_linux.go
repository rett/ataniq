//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// FD1Static is frozen before request creation. Channel identities deliberately
// do not belong in this structure or in planDigest.
type FD1Static struct {
	Directory    Boundary `json:"directory"`
	Leaf         Subject  `json:"leaf"`
	Challenge    FD1Leaf  `json:"challenge"`
	MetadataName string   `json:"metadata_name"`
}

type FD1Endpoint struct {
	FD       int          `json:"fd"`
	Identity FileIdentity `json:"identity"`
	Mode     uint32       `json:"mode"`
	Flags    int          `json:"flags"`
}

// This is evidence from the authenticated service-local injector, not an
// authority conveyed by the artifact itself. No source path or selection is
// accepted from the artifact. Endpoints are in the one fixed table order.
type FD1Runtime struct {
	Artifact      Subject       `json:"artifact"`
	Bindings      FD1Bindings   `json:"bindings"`
	Endpoints     []FD1Endpoint `json:"endpoints"`
	SourcesClosed bool          `json:"sources_closed"`
}

const fd1IntegrityFailure = "typed runtime artifact integrity unproved"

// Validate only durable structure here, including during cleanup transitions.
// A disappeared artifact must not erase independently validated stop authority.
func validateFD1Stored(a *Attempt) error {
	if err := validateFD1Runtime(a, a.FD1Runtime); err != nil {
		return err
	}
	if a.Injector != nil && !sameJSON(a.FD1Runtime, a.Injector.FD1Runtime) {
		return errors.New("stored typed runtime disagrees with injector")
	}
	if a.FD1IntegrityFailure != "" && (a.FD1IntegrityFailure != fd1IntegrityFailure || a.Config.DescriptorProfile != FD1TypedProfile || a.FD1Runtime == nil) {
		return errors.New("invalid typed integrity failure")
	}
	if a.Config.DescriptorProfile == FD1TypedProfile {
		switch a.Phase {
		case "bound", "released", "cancelling", "empty", "complete":
			if a.Injector == nil {
				return errors.New("missing original typed injector")
			}
		}
		if a.Injector == nil && (a.Workload != nil || a.Target != nil) {
			return errors.New("partial original typed launch binding")
		}
		if a.FD1Runtime != nil && a.Injector == nil {
			return errors.New("typed runtime without original injector")
		}
		if a.Injector != nil && validateInjection(a, *a.Injector, a.Workload) != nil {
			return errors.New("invalid stored typed injector")
		}
	}
	return nil
}

// Sticky evidence failure, independent of filesystem durability poisoning:
// cleanup may still save the original authority and the failure together.
func (c *Campaign) checkFD1Integrity(a *Attempt) {
	if a.FD1Runtime == nil {
		return
	}
	if authenticateFD1Runtime(a, a.FD1Runtime) != nil {
		a.FD1IntegrityFailure = fd1IntegrityFailure
		c.Blocked = true
	}
}

func needsDirectControl(a *Attempt) bool {
	return a.Config.Role == "candidate" && (a.Config.Case == "fake-fd" || a.Config.DescriptorProfile == FD1TypedProfile)
}

func freshFD1Control(a *Attempt, now int64) error {
	if a.Config.DescriptorProfile != FD1TypedProfile || !needsDirectControl(a) {
		return nil
	}
	c := a.Control
	if c == nil || c.Attempt != a.Config.DirectControl || c.RunID != a.Config.RunID || c.Boot != a.Boot || c.MaxAgeSeconds != 300 || c.DirectCreated == nil || *c.DirectCreated <= 0 || c.ValidatedAt < *c.DirectCreated || now < c.ValidatedAt || now-*c.DirectCreated > 300 {
		return errors.New("typed direct control expired before release")
	}
	return nil
}

func fd1LeafBytes(nonce string) []byte { return []byte("ataniq-fd1-leaf-v1 " + nonce + "\n") }

func prepareFD1Static(a *Attempt, path string) error {
	p := filepath.Join(path, "capability")
	if err := os.Mkdir(p, 0700); err != nil {
		return err
	}
	if err := exclusiveFile(filepath.Join(p, "leaf"), fd1LeafBytes(a.FixtureNonce), 0400); err != nil {
		return err
	}
	d, id, err := ownedDir(p)
	if err != nil {
		return err
	}
	if err = d.Close(); err != nil {
		return err
	}
	s, b, err := subject(filepath.Join(p, "leaf"), false, false, true, 4096)
	if err != nil {
		return err
	}
	size := int64(len(b))
	a.FD1Static = &FD1Static{Directory: Boundary{p, id}, Leaf: s, MetadataName: FD1BindingName,
		Challenge: FD1Leaf{Name: "leaf", Device: s.Identity.Device, Inode: s.Identity.Inode, Size: &size, SHA256: s.SHA256}}
	return nil
}

func validateFD1Static(a *Attempt, path string) error {
	s := a.FD1Static
	if a.Config.DescriptorProfile == "" {
		if s != nil || a.FD1Runtime != nil {
			return errors.New("unexpected typed evidence")
		}
		return nil
	}
	if a.Config.DescriptorProfile != FD1TypedProfile || a.DescriptorPolicy != FinalDescriptorPolicy || s == nil ||
		s.Directory.Path != filepath.Join(path, "capability") || s.Directory.Identity.Device == 0 || s.Directory.Identity.Inode == 0 || s.Directory.Identity.UID != uint32(os.Getuid()) ||
		s.Leaf.Path != filepath.Join(s.Directory.Path, "leaf") || s.Leaf.Identity.UID != uint32(os.Getuid()) || s.MetadataName != FD1BindingName ||
		!s.Challenge.valid(4096) || s.Challenge.Name != "leaf" || s.Challenge.Device != s.Leaf.Identity.Device || s.Challenge.Inode != s.Leaf.Identity.Inode ||
		*s.Challenge.Size != int64(len(fd1LeafBytes(a.FixtureNonce))) || s.Challenge.SHA256 != syntheticDigest(fd1LeafBytes(a.FixtureNonce)) || s.Leaf.SHA256 != s.Challenge.SHA256 || s.Leaf.ELF != (ELFMetadata{}) {
		return errors.New("invalid static typed challenge")
	}
	return nil
}

func verifyFD1Static(a *Attempt) error {
	if a.Config.DescriptorProfile != FD1TypedProfile || a.FD1Static == nil {
		return errors.New("missing fixed typed static fixture")
	}
	if err := validateFD1Static(a, filepath.Join(a.Config.Campaign, a.ID)); err != nil {
		return err
	}
	s := a.FD1Static
	d, id, err := ownedDir(s.Directory.Path)
	if err != nil {
		return err
	}
	if err = d.Close(); err != nil {
		return err
	}
	leaf, _, err := subject(s.Leaf.Path, false, false, true, 4096)
	if err != nil || id != s.Directory.Identity || leaf != s.Leaf {
		return errors.New("typed static fixture changed")
	}
	return nil
}

func fd1Objects(b FD1Bindings) []FD1Object {
	return []FD1Object{b.Directory.FD1Object, b.Pipe.FD1Object, b.Socket.FD1Object, b.Regular.FD1Object}
}

func fd1Kind(fd int) uint32 {
	switch fd {
	case FD1DirectoryFD:
		return syscall.S_IFDIR
	case FD1PipeFD:
		return syscall.S_IFIFO
	case FD1SocketFD:
		return syscall.S_IFSOCK
	default:
		return syscall.S_IFREG
	}
}

func fd1ExpectedFlags(fd int) int {
	switch fd {
	case FD1DirectoryFD:
		return syscall.O_DIRECTORY | syscall.O_NOFOLLOW | 0x8000 // Linux O_LARGEFILE
	case FD1PipeFD:
		return syscall.O_NONBLOCK | syscall.O_RDONLY
	case FD1SocketFD:
		return syscall.O_NONBLOCK | syscall.O_RDWR
	default:
		return syscall.O_NOFOLLOW | 0x8000
	}
}

// Pure and standalone: it must also work before a.Injector or a.FD1Runtime is
// assigned. Expectations come from the immutable plan and fixed role table.
func validateFD1Runtime(a *Attempt, r *FD1Runtime) error {
	if a.Config.DescriptorProfile == "" {
		if r != nil {
			return errors.New("unexpected typed runtime evidence")
		}
		return nil
	}
	if a.Config.DescriptorProfile != FD1TypedProfile {
		return errors.New("unknown runtime descriptor profile")
	}
	if r == nil {
		if a.Injector != nil || a.Phase == "bound" || a.Phase == "released" || a.Phase == "complete" {
			return errors.New("missing typed runtime binding")
		}
		return nil
	}
	if a.Config.DescriptorProfile != FD1TypedProfile || a.FD1Static == nil || r.Bindings.Validate() != nil || !r.SourcesClosed || len(r.Endpoints) != 4 {
		return errors.New("invalid typed runtime evidence")
	}
	if err := validateFD1Static(a, filepath.Join(a.Config.Campaign, a.ID)); err != nil {
		return err
	}
	b, s := r.Bindings, a.FD1Static
	if b.PlanSHA256 != a.PlanSHA256 || b.RunID != a.Config.RunID || b.RequestID != a.Config.RequestID || b.AttemptID != a.ID || b.Profile != a.Config.DescriptorProfile || b.Nonce != a.FixtureNonce ||
		!sameJSON(b.Directory.Leaf, &s.Challenge) || b.Directory.Device != s.Directory.Identity.Device || b.Directory.Inode != s.Directory.Identity.Inode ||
		b.Regular.Device != a.Fixture.Identity.Device || b.Regular.Inode != a.Fixture.Identity.Inode || b.Regular.SHA256 != a.Fixture.SHA256 || *b.Regular.Size != int64(len(fixtureBytes(a.FixtureNonce))) ||
		r.Artifact.Path != filepath.Join(a.Config.Campaign, a.ID, "fixtures", FD1BindingName) || r.Artifact.Identity.Device == 0 || r.Artifact.Identity.Inode == 0 || r.Artifact.Identity.UID != uint32(os.Getuid()) || !validDigest(r.Artifact.SHA256) || r.Artifact.ELF != (ELFMetadata{}) {
		return errors.New("typed runtime context mismatch")
	}
	seen := map[FileIdentity]bool{}
	for j, o := range fd1Objects(b) {
		e := r.Endpoints[j]
		if e.FD != o.FD || e.Identity != (FileIdentity{o.Device, o.Inode, uint32(os.Getuid())}) || e.Mode&syscall.S_IFMT != fd1Kind(o.FD) || e.Flags != fd1ExpectedFlags(o.FD) || seen[e.Identity] {
			return errors.New("typed endpoint owner/type/flags mismatch")
		}
		seen[e.Identity] = true
	}
	return nil
}

func authenticateFD1Runtime(a *Attempt, r *FD1Runtime) error {
	if a.FD1IntegrityFailure != "" {
		return errors.New(fd1IntegrityFailure)
	}
	if a.Config.DescriptorProfile == "" {
		return validateFD1Runtime(a, r)
	}
	if r == nil {
		return errors.New("missing typed runtime artifact")
	}
	if err := validateFD1Runtime(a, r); err != nil {
		return err
	}
	s, data, err := subject(r.Artifact.Path, false, false, true, MaxFD1BindingBytes)
	if err != nil || s != r.Artifact {
		return errors.New("typed artifact subject changed")
	}
	fi, err := os.Lstat(r.Artifact.Path)
	if err != nil || fi.Mode().Perm() != 0400 || identity(fi) != s.Identity {
		return errors.New("typed artifact not frozen read-only")
	}
	b, err := DecodeFD1Bindings(bytes.NewReader(data))
	if err != nil || !sameJSON(b, r.Bindings) {
		return errors.New("typed artifact content mismatch")
	}
	return nil
}

func validateFD1Control(a, d *Attempt, now int64) error {
	if d == nil || d.Config.DescriptorProfile != FD1TypedProfile || d.Config.Case != a.Config.Case || d.Config.Campaign != a.Config.Campaign || d.Campaign != a.Campaign ||
		d.DescriptorPolicy != FinalDescriptorPolicy || a.DescriptorPolicy != d.DescriptorPolicy || d.Config.Role != "direct" || d.Phase != "complete" || d.Config.RunID != a.Config.RunID || d.Boot != a.Boot || now < d.Created || now-d.Created > 300 ||
		d.Profile != a.Profile || !attemptID(d.FixtureNonce) || !d.Empty || !d.PopulatedLive || !d.TargetExited || !d.SliceStopped || d.Injector == nil || d.DescriptorReceipt == nil || d.Observation == nil ||
		validateDescriptorReceipt(d, *d.DescriptorReceipt) != nil || validateDescriptorObservations(d, *d.Observation) != nil || authenticateFD1Runtime(d, d.FD1Runtime) != nil {
		return errors.New("invalid scoped typed direct control")
	}
	for _, n := range []string{"driver", "probe", "bwrap", "bpf"} {
		if d.Subjects[n].SHA256 != a.Subjects[n].SHA256 {
			return errors.New("typed control chain mismatch")
		}
	}
	if d.Observation.Validate() != nil || d.Observation.RunID != d.Config.RunID || d.Observation.RequestID != d.Config.RequestID || d.Observation.Case != d.Request.Case || d.Observation.Status != "observed" {
		return errors.New("typed control observation mismatch")
	}
	return nil
}
