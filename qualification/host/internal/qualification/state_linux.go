//go:build linux && amd64

package qualification

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Boundary struct {
	Path     string       `json:"path"`
	Identity FileIdentity `json:"identity"`
}

type Process struct {
	Boot       string                  `json:"boot"`
	PID        int                     `json:"pid"`
	Start      uint64                  `json:"start"`
	Executable FileIdentity            `json:"executable"`
	Cgroup     string                  `json:"cgroup"`
	Namespaces map[string]FileIdentity `json:"namespaces"`
	Status     string                  `json:"status"`
	UIDMap     string                  `json:"uid_map"`
	GIDMap     string                  `json:"gid_map"`
	Mounts     MountState              `json:"mounts"`
}

type MountState struct {
	Root    string `json:"root"`
	Probe   string `json:"probe"`
	Fixture string `json:"fixture"`
	Proc    string `json:"proc"`
}

type Unit struct {
	Properties map[string]string `json:"properties"`
	Boundary   Boundary          `json:"boundary"`
	Main       *Process          `json:"main,omitempty"`
}

type Attempt struct {
	FD1Static             *FD1Static              `json:"fd1_static,omitempty"`
	FD1Runtime            *FD1Runtime             `json:"fd1_runtime,omitempty"`
	FD1IntegrityFailure   string                  `json:"fd1_integrity_failure,omitempty"`
	DescriptorPolicy      string                  `json:"descriptor_policy,omitempty"`
	TargetCommand         []string                `json:"target_command,omitempty"`
	DescriptorReceipt     *DescriptorReceipt      `json:"descriptor_receipt,omitempty"`
	DescriptorObservation *DescriptorObservation  `json:"descriptor_observation,omitempty"`
	FreshService          map[string]string       `json:"fresh_service,omitempty"`
	FreshSlice            map[string]string       `json:"fresh_slice,omitempty"`
	Boot                  string                  `json:"boot"`
	FixtureNonce          string                  `json:"fixture_nonce"`
	Control               *ControlBinding         `json:"control,omitempty"`
	Version               int                     `json:"version"`
	ID                    string                  `json:"id"`
	Root                  FileIdentity            `json:"root"`
	Campaign              FileIdentity            `json:"campaign"`
	Config                HostConfig              `json:"config"`
	Profile               string                  `json:"profile"`
	Phase                 string                  `json:"phase"`
	Stage                 string                  `json:"stage,omitempty"`
	ReasonCode            string                  `json:"reason_code,omitempty"`
	Reason                string                  `json:"reason,omitempty"`
	Created               int64                   `json:"created"`
	NotAfter              int64                   `json:"not_after"`
	Slice                 string                  `json:"slice"`
	Service               string                  `json:"service"`
	Subjects              map[string]Subject      `json:"subjects"`
	Snapshots             map[string]Subject      `json:"snapshots"`
	Fixture               Subject                 `json:"fixture"`
	Streams               map[string]FileIdentity `json:"streams"`
	Command               []string                `json:"command"`
	Launch                []string                `json:"launch"`
	Cleanup               []string                `json:"cleanup"`
	PlanSHA256            string                  `json:"plan_sha256"`
	Request               Request                 `json:"request"`
	Parent                *Unit                   `json:"parent,omitempty"`
	Workload              *Unit                   `json:"workload,omitempty"`
	Injector              *Injection              `json:"injector,omitempty"`
	Target                *Process                `json:"target,omitempty"`
	ExecMain              *Process                `json:"exec_main,omitempty"`
	PopulatedLive         bool                    `json:"populated_live"`
	TargetExited          bool                    `json:"target_exited"`
	Empty                 bool                    `json:"empty"`
	SliceStopped          bool                    `json:"slice_stopped"`
	Observation           *Observation            `json:"observation,omitempty"`
	Ready                 *HoldFrame              `json:"ready,omitempty"`
	Final                 *HoldFrame              `json:"final,omitempty"`
}

type ControlBinding struct {
	Attempt       string `json:"attempt"`
	RunID         string `json:"run_id"`
	Boot          string `json:"boot"`
	ValidatedAt   int64  `json:"validated_at"`
	MaxAgeSeconds int64  `json:"max_age_seconds"`
	PlanSHA256    string `json:"plan_sha256"`
	DirectCreated *int64 `json:"direct_created,omitempty"`
}

// Campaign owns a single flock for its entire lifetime. A failed durable update
// poisons this controller; unresolved on-disk state also fences the next controller.
type Campaign struct {
	Path     string
	Identity FileIdentity
	dir      *os.File
	lock     *os.File
	Records  map[string]*Attempt
	Blocked  bool
	fenced   bool
	syncFile func(*os.File) error
	syncDir  func(*os.File) error
	rename   func(int, string, int, string) error
	updateID func() (string, error)
	// Narrow seam for post-adoption authentication/restoration race tests.
	authenticateFD1 func(*Attempt, *FD1Runtime) error
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func OpenCampaign(path string) (*Campaign, error) {
	d, id, err := ownedDir(path)
	if err != nil {
		return nil, err
	}
	c := &Campaign{Path: path, Identity: id, dir: d, Records: map[string]*Attempt{}, syncFile: (*os.File).Sync, syncDir: (*os.File).Sync, rename: syscall.Renameat, updateID: randomID, authenticateFD1: authenticateFD1Runtime}
	fd, err := syscall.Openat(int(d.Fd()), "controller.lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		d.Close()
		return nil, err
	}
	c.lock = os.NewFile(uintptr(fd), "controller.lock")
	fi, err := c.lock.Stat()
	if err != nil || checkRegular(fi, 0) != nil || identity(fi).UID != id.UID || fi.Mode().Perm() != 0600 {
		c.Close()
		return nil, errors.New("invalid controller lock")
	}
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		c.Close()
		return nil, errors.New("campaign already controlled")
	}
	if err = c.dir.Sync(); err != nil {
		c.Close()
		return nil, err
	}
	entries, err := d.ReadDir(MaxEntries + 1)
	if err != nil || len(entries) > MaxEntries {
		c.Close()
		return nil, errors.New("campaign scan failed or excessive")
	}
	for _, e := range entries {
		if e.Name() == "controller.lock" {
			continue
		}
		if !attemptID(e.Name()) || !e.IsDir() {
			c.Blocked = true
			c.fenced = true
			continue
		}
		a, err := c.read(e.Name())
		if err != nil {
			c.Blocked = true
			c.fenced = true
			continue
		}
		c.Records[a.ID] = a
		if a.Phase != "complete" || a.FD1IntegrityFailure != "" {
			c.Blocked = true
		}
	}
	return c, nil
}

func (c *Campaign) Close() {
	if c.lock != nil {
		c.lock.Close()
		c.lock = nil
	}
	if c.dir != nil {
		c.dir.Close()
		c.dir = nil
	}
}

func (c *Campaign) poison(dir *os.File) {
	c.Blocked = true
	c.fenced = true
	// Best effort only: a failing filesystem cannot promise a new durable write.
	// The pre-effect unresolved intent remains the primary crash fence. A visible
	// sync-failed marker also fences a completion whose last parent sync failed.
	if dir != nil {
		_ = failureMarker(dir)
	}
}

// Only a pinned, identity-validated owned attempt directory may receive this
// marker. Never construct a marker pathname from rejected ownership or IDs.
func failureMarker(dir *os.File) error {
	fd, err := syscall.Openat(int(dir.Fd()), "sync-failed", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0400)
	if os.IsExist(err) {
		// Any entry with this name fences startup; do not follow or rewrite it.
		return dir.Sync()
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "sync-failed")
	n, err := f.WriteString("durability unknown\n")
	if err == nil && n != len("durability unknown\n") {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return dir.Sync()
}

func (c *Campaign) checkRoot() error {
	d, id, err := ownedDir(c.Path)
	if err != nil {
		c.poison(nil)
		return err
	}
	d.Close()
	if id != c.Identity {
		c.poison(nil)
		return errors.New("campaign identity changed")
	}
	return nil
}

func phaseValid(s string) bool {
	switch s {
	case "prepared", "launching-slice", "slice-bound", "launching-service", "bound", "released", "cancelling", "empty", "complete", "launch-unknown", "cleanup-unknown":
		return true
	}
	return false
}

func (c *Campaign) read(id string) (*Attempt, error) {
	path := filepath.Join(c.Path, id)
	d, root, err := ownedDir(path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	b, err := readOwned(filepath.Join(path, "intent.json"), MaxInputBytes)
	if err != nil {
		return nil, err
	}
	var a Attempt
	if err = decodeHostRecord(bytes.NewReader(b), &a); err != nil {
		return nil, err
	}
	if a.Version != 1 || a.ID != id || a.Root != root || a.Campaign != c.Identity || a.Config.Campaign != c.Path || a.Config.validate() != nil || a.Profile != HostProfile || !phaseValid(a.Phase) || a.Created <= 0 || a.NotAfter != a.Created+60 || a.Slice != "aq"+id+".slice" || a.Service != "aq"+id+".service" || len(a.Subjects) != 6 || a.Request.Validate() != nil {
		return nil, errors.New("invalid attempt ownership or intent")
	}
	if a.Phase == "complete" && (!a.Empty || !a.PopulatedLive || !a.SliceStopped || a.Parent == nil || a.Workload == nil || a.Injector == nil || a.Target == nil || !a.TargetExited) {
		return nil, errors.New("invalid completion")
	}
	if err = validatePlan(&a, path); err != nil {
		return nil, err
	}
	// Current artifact integrity is separate from the durable stop authority.
	// Keep structurally validated records inspectable and cleanup-only.
	c.checkFD1Integrity(&a)
	if _, err := os.Lstat(filepath.Join(path, "sync-failed")); !os.IsNotExist(err) {
		return nil, errors.New("previous durability failure")
	}
	return &a, nil
}

func (c *Campaign) Save(a *Attempt, phase, reason string) error {
	if a == nil || !attemptID(a.ID) || !phaseValid(phase) || !phaseValid(a.Phase) || a.Version != 1 || a.Campaign != c.Identity || a.Config.Campaign != c.Path {
		c.poison(nil)
		return errors.New("invalid attempt identity or transition")
	}
	if recorded := c.Records[a.ID]; recorded == nil || recorded.Root != a.Root || recorded.Campaign != a.Campaign {
		c.poison(nil)
		return errors.New("attempt is not recorded with this identity")
	}
	next := *a
	next.Phase = phase
	if err := validateFD1Stored(&next); err != nil {
		c.poison(nil)
		return err
	}
	c.checkFD1Integrity(a)
	if a.Config.DescriptorProfile == FD1TypedProfile && (phase == "bound" || phase == "released") && a.FD1IntegrityFailure != "" {
		return errors.New("typed runtime not durably bindable")
	}
	if err := c.checkRoot(); err != nil {
		return err
	}
	d, root, err := ownedDir(filepath.Join(c.Path, a.ID))
	if err != nil {
		c.poison(nil)
		return err
	}
	defer d.Close()
	if root != a.Root {
		c.poison(nil)
		return errors.New("attempt identity changed")
	}
	if c.fenced {
		// An earlier failure may have been unable to write a safe marker. Before
		// allowing any later durable update, carry that fence across restart.
		if err = failureMarker(d); err != nil {
			return err
		}
	}
	// Records may share a with the caller. Do not publish a completion in memory
	// until the replacement and parent sync succeed; the prior intent stays live.
	next = *a
	next.Phase = phase
	next.Reason = reason
	b, err := json.Marshal(&next)
	if err != nil || len(b) > MaxInputBytes {
		c.poison(d)
		return errors.New("intent encoding failed")
	}
	tmp, err := c.updateID()
	if err != nil {
		c.poison(d)
		return err
	}
	tmp = "update" + tmp
	err = c.write(d, tmp, b)
	if err == nil {
		err = c.rename(int(d.Fd()), tmp, int(d.Fd()), "intent.json")
	}
	if err == nil {
		err = c.syncDir(d)
	}
	if err != nil {
		c.poison(d)
		return err
	}
	a.Phase = next.Phase
	a.Reason = next.Reason
	c.Records[a.ID] = a
	if phase == "complete" {
		c.Blocked = c.fenced
		for _, r := range c.Records {
			if r.Phase != "complete" || r.FD1IntegrityFailure != "" {
				c.Blocked = true
			}
		}
	}
	return nil
}

func (c *Campaign) write(dir *os.File, name string, b []byte) error {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = c.syncFile(f)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func (c *Campaign) create(a *Attempt) error {
	if a == nil || !attemptID(a.ID) || !phaseValid(a.Phase) {
		c.poison(nil)
		return errors.New("invalid attempt identity or phase")
	}
	if err := c.checkRoot(); err != nil {
		return err
	}
	if c.Blocked || len(c.Records) >= MaxEntries-2 {
		return errors.New("new starts blocked")
	}
	if err := syscall.Mkdirat(int(c.dir.Fd()), a.ID, 0700); err != nil {
		c.poison(nil)
		return err
	}
	c.Blocked = true // An orphan directory is deliberately restart-blocking.
	if err := c.syncDir(c.dir); err != nil {
		c.poison(nil)
		return err
	}
	d, root, err := ownedDir(filepath.Join(c.Path, a.ID))
	if err != nil {
		c.poison(nil)
		return err
	}
	defer d.Close()
	a.Root = root
	a.Campaign = c.Identity
	b, err := json.Marshal(a)
	if err != nil || len(b) > MaxInputBytes {
		c.poison(d)
		return errors.New("intent encoding failed")
	}
	if err = c.write(d, "intent.json", b); err != nil {
		c.poison(d)
		return err
	}
	if err = c.syncDir(d); err != nil {
		c.poison(d)
		return err
	}
	c.Records[a.ID] = a
	return nil
}

func (c *Campaign) fresh(config HostConfig) (*Attempt, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	a := &Attempt{Version: 1, ID: id, Config: config, Profile: HostProfile, Phase: "prepared", Created: now, NotAfter: now + 60, Slice: "aq" + id + ".slice", Service: "aq" + id + ".service", Subjects: map[string]Subject{}}
	return a, nil
}
