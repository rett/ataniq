//go:build linux && amd64

package qualification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func fixtureBytes(nonce string) []byte { return []byte("ataniq-synthetic-regular-v1 " + nonce + "\n") }

func readOwned(path string, limit int64) ([]byte, error) {
	s, b, err := subject(path, false, false, true, limit)
	if err != nil {
		return nil, err
	}
	if s.Identity.UID != uint32(os.Getuid()) {
		return nil, errors.New("unowned evidence")
	}
	return b, nil
}

func exclusiveFile(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func selected(c HostConfig) map[string]string {
	return map[string]string{"driver": c.Driver, "probe": c.Probe, "bwrap": c.Bwrap, "bpf": c.BPF, "systemctl": c.Systemctl, "systemd-run": c.SystemdRun}
}

// Atomic publication without overwriting an existing evidence/release name.
// renameat2(RENAME_NOREPLACE) never exposes a partly written handshake file.
func publishOwned(dir, name string, b []byte) error {
	if !validName(name) {
		return errors.New("invalid publication name")
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	from := filepath.Join(dir, "publish"+id)
	if err = exclusiveFile(from, b, 0400); err != nil {
		return err
	}
	src, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := syscall.BytePtrFromString(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(316, ^uintptr(99), uintptr(unsafe.Pointer(src)), ^uintptr(99), uintptr(unsafe.Pointer(dst)), 1, 0)
	if errno != 0 {
		return errno
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (c *Campaign) Prepare(config HostConfig) (*Attempt, error) {
	if config.validate() != nil || config.Campaign != c.Path || c.Blocked {
		return nil, errors.New("invalid config or new starts blocked")
	}
	a, err := c.fresh(config)
	if err != nil {
		return nil, err
	}
	a.Campaign = c.Identity
	a.DescriptorPolicy = FinalDescriptorPolicy
	a.Boot, err = bootID()
	if err != nil {
		return nil, err
	}
	a.FixtureNonce, err = randomID()
	if err != nil {
		return nil, err
	}
	paths := selected(config)
	for _, name := range []string{"driver", "probe", "bwrap", "bpf", "systemctl", "systemd-run"} {
		path := paths[name]
		s, _, err := chainSubject(name, path, false)
		if err != nil {
			return nil, &hostFailure{"subject-" + name, "subject-rejected", err}
		}
		a.Subjects[name] = s
		if name == "bpf" {
			_, b, e := subject(path, false, false, false, 32768)
			if e != nil || len(b) == 0 || len(b)%8 != 0 {
				return nil, errors.New("bounded reviewed classic BPF required")
			}
		}
	}
	if needsDirectControl(a) {
		d := c.Records[config.DirectControl]
		if validateControl(a, d, time.Now().Unix()) != nil {
			return nil, errors.New("valid matching direct control required")
		}
		a.FixtureNonce = d.FixtureNonce
		a.Control = &ControlBinding{Attempt: d.ID, RunID: config.RunID, Boot: a.Boot, ValidatedAt: time.Now().Unix(), MaxAgeSeconds: 300, PlanSHA256: d.PlanSHA256}
		if config.DescriptorProfile == FD1TypedProfile {
			created := d.Created
			a.Control.DirectCreated = &created
		}
	}
	path := filepath.Join(c.Path, a.ID)
	a.Command = command(a, path)
	a.TargetCommand = targetCommand(a, path)
	a.Launch = launch(a, path)
	a.Cleanup = []string{"stop " + a.Service, "stop " + a.Slice}
	// Exclusive, synced initial intent precedes even supervisor inspection.
	if err = c.create(a); err != nil {
		return nil, err
	}
	a.Snapshots = map[string]Subject{}
	for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
		s, b, err := chainSubject(name, a.Subjects[name].Path, false)
		if err != nil || s != a.Subjects[name] {
			return nil, errors.New("subject changed before freezing")
		}
		mode := os.FileMode(0500)
		if name == "bpf" {
			mode = 0400
		}
		p := filepath.Join(path, name)
		if err = exclusiveFile(p, b, mode); err != nil {
			return nil, err
		}
		f, _, err := chainSubject(name, p, true)
		if err != nil {
			return nil, err
		}
		a.Snapshots[name] = f
	}
	if err = os.Mkdir(filepath.Join(path, "fixtures"), 0700); err != nil {
		return nil, err
	}
	p := filepath.Join(path, "fixtures", "fake-file")
	if err = exclusiveFile(p, fixtureBytes(a.FixtureNonce), 0400); err != nil {
		return nil, err
	}
	a.Fixture, _, err = subject(p, false, false, true, 4096)
	if err != nil {
		return nil, err
	}
	if config.DescriptorProfile == FD1TypedProfile {
		if err = prepareFD1Static(a, path); err != nil {
			return nil, err
		}
	}
	a.Request = hostRequest(a)
	request, _ := json.Marshal(a.Request)
	if err = exclusiveFile(filepath.Join(path, "request.json"), request, 0400); err != nil {
		return nil, err
	}
	if err = exclusiveFile(filepath.Join(path, "output"), nil, 0600); err != nil {
		return nil, err
	}
	if err = exclusiveFile(filepath.Join(path, "stderr"), nil, 0600); err != nil {
		return nil, err
	}
	a.Streams = map[string]FileIdentity{}
	for _, name := range []string{"request.json", "output", "stderr"} {
		s, _, err := subject(filepath.Join(path, name), false, false, true, MaxOutputBytes)
		if err != nil {
			return nil, err
		}
		key := name
		if name == "request.json" {
			key = "request"
		}
		a.Streams[key] = s.Identity
	}
	a.PlanSHA256 = planDigest(a)
	if err = c.Save(a, "prepared", ""); err != nil {
		return nil, err
	}
	return a, nil
}

func hostRequest(a *Attempt) Request {
	c := a.Config
	r := Request{Version: 1, RunID: c.RunID, RequestID: c.RequestID, Case: Case(c.Case), MaxDurationMS: 1500, MaxBytes: 4096}
	switch c.Case {
	case "hold":
		r.Case = CaseInventory
	case "fake-file":
		r.Payload = &Payload{File: &NamePayload{Name: "fake-file"}}
	case "fake-fd":
		fd, reg := FakeFD, true
		dev, ino := a.Fixture.Identity.Device, a.Fixture.Identity.Inode
		r.Payload = &Payload{FD: &FDPayload{Name: "fake-file", FD: &fd, Regular: &reg, Device: &dev, Inode: &ino, SHA256: a.Fixture.SHA256}}
	case "fake-dir-fd", "fake-pipe-fd", "fake-socket-fd":
		p := &TypedFDPayload{Name: FD1BindingName, FD: typedFDSlot(r.Case), Profile: FD1TypedProfile, AttemptID: a.ID, Nonce: a.FixtureNonce}
		if r.Case == CaseFakeDirFD && a.FD1Static != nil {
			p.Leaf = &a.FD1Static.Challenge
		}
		r.Payload = &Payload{TypedFD: p}
	}
	return r
}

func command(a *Attempt, path string) []string {
	if a.DescriptorPolicy != "" {
		return []string{filepath.Join(path, "driver"), "private-final-descriptors", path}
	}
	return targetCommand(a, path)
}

func targetCommand(a *Attempt, path string) []string {
	args := []string{filepath.Join(path, "probe"), "--fixture-root", filepath.Join(path, "fixtures")}
	if a.Config.Role != "direct" {
		args = append([]string{filepath.Join(path, "bwrap")}, BubblewrapArgs(filepath.Join(path, "probe"), filepath.Join(path, "fixtures", "fake-file"))...)
		if a.Config.DescriptorProfile == FD1TypedProfile {
			// Only the artifact is exposed by pathname. The capability tree never
			// appears in bwrap's mount arguments.
			at := len(args) - 8 // before --proc /proc and the final target tail
			for j, arg := range args {
				if arg == "--proc" {
					at = j
					break
				}
			}
			extra := []string{"--ro-bind", filepath.Join(path, "fixtures", FD1BindingName), "/fixtures/" + FD1BindingName}
			args = append(args[:at], append(extra, args[at:]...)...)
		}
	}
	if a.Config.Case == "hold" {
		args = append(args, "--private-ready-hold")
	}
	return args
}

func launch(a *Attempt, path string) []string {
	return []string{"--user", "--quiet", "--unit=" + a.Service, "--slice=" + a.Slice, "--service-type=exec",
		"--property=Restart=no", "--property=RemainAfterExit=no",
		"--property=Description=ataniq-qualification-" + a.ID,
		"--property=RuntimeMaxSec=20s", "--property=TimeoutStopSec=3s", "--property=KillMode=control-group",
		"--property=MemoryMax=67108864", "--property=TasksMax=32", "--property=CPUQuota=100%",
		"--property=NoNewPrivileges=yes", "--property=LimitNOFILE=1024", "--property=LimitFSIZE=131072",
		"--property=StandardInput=file:" + filepath.Join(path, "request.json"),
		"--property=StandardOutput=append:" + filepath.Join(path, "output"),
		"--property=StandardError=append:" + filepath.Join(path, "stderr"),
		"--", filepath.Join(path, "driver"), "private-inject", path}
}

func validatePlan(a *Attempt, path string) error {
	if err := validateFD1Static(a, path); err != nil {
		return err
	}
	if a.DescriptorPolicy != "" && a.DescriptorPolicy != FinalDescriptorPolicy {
		return errors.New("unknown descriptor policy")
	}
	if a.DescriptorPolicy == "" && (len(a.TargetCommand) != 0 || a.Config.Role == "weakened-synthetic") || a.DescriptorPolicy != "" && !reflect.DeepEqual(a.TargetCommand, targetCommand(a, path)) {
		return errors.New("changed final target")
	}
	if !validBoot(a.Boot) || !attemptID(a.FixtureNonce) {
		return errors.New("missing boot or fixture nonce")
	}
	if !reflect.DeepEqual(a.Command, command(a, path)) || !reflect.DeepEqual(a.Launch, launch(a, path)) {
		return errors.New("changed launch plan")
	}
	if !reflect.DeepEqual(a.Cleanup, []string{"stop " + a.Service, "stop " + a.Slice}) || a.PlanSHA256 != planDigest(a) {
		return errors.New("changed cleanup authority or plan digest")
	}
	for name, p := range selected(a.Config) {
		s, ok := a.Subjects[name]
		if !ok || s.Path != p || !bounded(s.SHA256, 64, 64, isHex) || s.Identity.Inode == 0 {
			return errors.New("invalid subjects")
		}
	}
	if len(a.Snapshots) != 4 {
		return errors.New("incomplete frozen chain")
	}
	for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
		s := a.Snapshots[name]
		if s.Path != filepath.Join(path, name) || s.Identity.Inode == 0 || s.Identity.UID != uint32(os.Getuid()) || s.SHA256 != a.Subjects[name].SHA256 || s.ELF != a.Subjects[name].ELF {
			return errors.New("invalid frozen chain")
		}
	}
	if a.Fixture.Path != filepath.Join(path, "fixtures", "fake-file") || a.Fixture.Identity.Inode == 0 || a.Fixture.Identity.UID != uint32(os.Getuid()) || a.Request.RunID != a.Config.RunID || a.Request.RequestID != a.Config.RequestID {
		return errors.New("invalid fixture or request")
	}
	if !reflect.DeepEqual(a.Request, hostRequest(a)) {
		return errors.New("request differs from selected case")
	}
	h := sha256.Sum256(fixtureBytes(a.FixtureNonce))
	if a.Fixture.SHA256 != hex.EncodeToString(h[:]) {
		return errors.New("fixture nonce digest mismatch")
	}
	if err := validateStored(a); err != nil {
		return err
	}
	return nil
}

func planDigest(a *Attempt) string {
	b, _ := json.Marshal(struct {
		Config                   HostConfig
		Profile                  string
		Command, Launch, Cleanup []string
		Subjects, Snapshots      map[string]Subject
		Fixture                  Subject
		Request                  Request
		Streams                  map[string]FileIdentity
		Boot, FixtureNonce       string
		Control                  *ControlBinding
		DescriptorPolicy         string     `json:",omitempty"`
		TargetCommand            []string   `json:",omitempty"`
		FD1Static                *FD1Static `json:",omitempty"`
	}{a.Config, a.Profile, a.Command, a.Launch, a.Cleanup, a.Subjects, a.Snapshots, a.Fixture, a.Request, a.Streams, a.Boot, a.FixtureNonce, a.Control, a.DescriptorPolicy, a.TargetCommand, a.FD1Static})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validProcess(p Process) bool {
	if p.PID < 1 || p.Start == 0 || !validBoot(p.Boot) || p.Executable.Inode == 0 || !strings.HasPrefix(p.Cgroup, "/") || len(p.Namespaces) != 6 || len(p.Status) > 16384 || p.UIDMap == "" || p.GIDMap == "" || p.Mounts.Root == "" {
		return false
	}
	for _, n := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
		if p.Namespaces[n].Inode == 0 {
			return false
		}
	}
	return true
}

func validateStored(a *Attempt) error {
	if err := validateFD1Runtime(a, a.FD1Runtime); err != nil {
		return err
	}
	if a.Injector != nil && !sameJSON(a.FD1Runtime, a.Injector.FD1Runtime) {
		return errors.New("stored typed runtime disagrees with injector")
	}
	if a.Config.DescriptorProfile == "" && a.Control != nil && a.Control.DirectCreated != nil {
		return errors.New("unexpected typed control evidence")
	}
	if a.Config.DescriptorProfile == FD1TypedProfile && needsDirectControl(a) && (a.Control == nil || a.Control.DirectCreated == nil || *a.Control.DirectCreated <= 0 || *a.Control.DirectCreated > a.Control.ValidatedAt || a.Control.ValidatedAt-*a.Control.DirectCreated > 300) {
		return errors.New("invalid typed control freshness binding")
	}
	if a.DescriptorReceipt != nil && validateDescriptorReceipt(a, *a.DescriptorReceipt) != nil {
		return errors.New("invalid stored descriptor receipt")
	}
	if a.DescriptorObservation != nil {
		if a.DescriptorPolicy != FinalDescriptorPolicy || a.Observation == nil || a.Config.Case != "inventory" || !sameJSON(*a.DescriptorObservation, observeDescriptorIdentities(a, *a.Observation)) {
			return errors.New("invalid stored descriptor observations")
		}
	}
	if needsDirectControl(a) && (a.Control == nil || a.Control.Attempt != a.Config.DirectControl || a.Control.RunID != a.Config.RunID || a.Control.Boot != a.Boot || a.Control.MaxAgeSeconds != 300 || a.Control.ValidatedAt < a.Created || !bounded(a.Control.PlanSHA256, 64, 64, isHex)) {
		return errors.New("missing scoped direct control")
	}
	if len(a.Streams) != 3 {
		return errors.New("invalid stream identities")
	}
	for _, name := range []string{"request", "output", "stderr"} {
		if a.Streams[name].Inode == 0 || a.Streams[name].UID != uint32(os.Getuid()) {
			return errors.New("invalid stream identity")
		}
	}
	if a.Parent != nil {
		p := a.Parent
		if p.Properties["Id"] != a.Slice || p.Properties["ActiveState"] != "active" || p.Properties["StopWhenUnneeded"] != "no" || p.Boundary.Identity.Inode == 0 || filepath.Base(p.Boundary.Path) != a.Slice {
			return errors.New("invalid stored parent")
		}
	}
	if a.Workload != nil {
		u := a.Workload
		if a.Parent == nil || u.Properties["Id"] != a.Service || u.Properties["Slice"] != a.Slice || !bounded(u.Properties["InvocationID"], 32, 32, isHex) || u.Boundary.Path != a.Parent.Boundary.Path+"/"+a.Service || u.Boundary.Identity.Inode == 0 || u.Main == nil || !validProcess(*u.Main) || u.Main.Cgroup != u.Boundary.Path {
			return errors.New("invalid stored service")
		}
	}
	if a.Injector != nil && (a.Workload == nil || validateInjection(a, *a.Injector, a.Workload) != nil) {
		return errors.New("invalid stored injector")
	}
	if a.Target != nil && (!validProcess(*a.Target) || a.Target.Boot != a.Boot || a.Workload == nil || a.Target.Cgroup != a.Workload.Boundary.Path) {
		return errors.New("invalid stored target")
	}
	if a.Empty && (!a.TargetExited || !a.PopulatedLive || a.Target == nil || a.Injector == nil) {
		return errors.New("invalid stored empty proof")
	}
	switch a.Phase {
	case "slice-bound", "launching-service":
		if a.Parent == nil {
			return errors.New("missing parent binding")
		}
	case "bound", "released", "cancelling", "empty", "complete":
		if a.Injector == nil || a.Target == nil || a.Workload == nil {
			return errors.New("missing launch binding")
		}
	}
	if a.Ready != nil && (a.Target == nil || validateReady(a, *a.Ready, *a.Target) != nil) {
		return errors.New("invalid stored readiness")
	}
	if a.Observation != nil && (a.Observation.Validate() != nil || a.Observation.RunID != a.Config.RunID || a.Observation.RequestID != a.Config.RequestID || a.Observation.Case != a.Request.Case) {
		return errors.New("invalid stored observation")
	}
	return nil
}

func factValue(o Observation, name string) string {
	for _, f := range o.Facts {
		if f.Name == name && f.State == "known" && f.Value != nil {
			return *f.Value
		}
	}
	return ""
}

func validateControl(a, d *Attempt, now int64) error {
	if a.Config.DescriptorProfile == FD1TypedProfile {
		return validateFD1Control(a, d, now)
	}
	if d != nil && d.Config.DescriptorProfile != "" {
		return errors.New("control profile mismatch")
	}
	if d != nil && (a.DescriptorPolicy != d.DescriptorPolicy || a.DescriptorPolicy != FinalDescriptorPolicy) {
		return errors.New("control descriptor policy mismatch")
	}
	if d != nil && (d.DescriptorReceipt == nil || validateDescriptorReceipt(d, *d.DescriptorReceipt) != nil) {
		return errors.New("control missing current descriptor receipt")
	}
	if d == nil || d.Config.Campaign != a.Config.Campaign || d.Campaign != a.Campaign || d.Phase != "complete" || d.Config.Role != "direct" || d.Config.Case != "fake-fd" || d.Config.RunID != a.Config.RunID || d.Boot != a.Boot || now < d.Created || now-d.Created > 300 || d.Observation == nil || d.Observation.Status != "observed" || factValue(*d.Observation, "digest_match") != "true" || d.Injector == nil || !d.Empty || !d.TargetExited || !d.PopulatedLive || !d.SliceStopped || d.Profile != a.Profile || !attemptID(d.FixtureNonce) {
		return errors.New("invalid scoped direct control")
	}
	for _, n := range []string{"driver", "probe", "bwrap", "bpf"} {
		if d.Subjects[n].SHA256 != a.Subjects[n].SHA256 {
			return errors.New("control chain mismatch")
		}
	}
	return nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
