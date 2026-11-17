//go:build linux && amd64

package qualification

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const R1EntryCase = "r1-entry"
const R1EntryProfile = "r1-entry-no-pressure-v1"
const entryIntegrityFailure = "entry control or evidence integrity failed"

// The entry profile has no pressure/finish operation or caller-selected budget.
// Static fields are omitted entirely from legacy plans, preserving their bytes.
type R1EntryStatic struct {
	Profile       string                 `json:"profile"`
	Request       R1Request              `json:"request"`
	RequestBytes  json.RawMessage        `json:"request_bytes"`
	RequestSHA256 string                 `json:"request_sha256"`
	Control       FileIdentity           `json:"control"`
	Null          R1NullDevice           `json:"null"`
	ManagerELF    map[string]ELFMetadata `json:"manager_elf"`
}

type R1NullDevice struct {
	Identity FileIdentity `json:"identity"`
	Mode     uint32       `json:"mode"`
	GID      uint32       `json:"gid"`
	Rdev     uint64       `json:"rdev"`
}

type R1EntryRuntime struct {
	// Dispatch facts survive progress/error labels and confer cleanup only on
	// restart. Neither fact permits replay of the corresponding operation.
	ParentStartIntent  bool              `json:"parent_start_intent,omitempty"`
	ToolDispatchIntent bool              `json:"tool_dispatch_intent,omitempty"`
	ServiceStopIntent  bool              `json:"service_stop_intent,omitempty"`
	ServiceStopped     bool              `json:"service_stopped,omitempty"`
	FailureReason      string            `json:"failure_reason,omitempty"`
	Host               *R1EntryHost      `json:"host,omitempty"`
	Admission          *entryAdmission   `json:"admission,omitempty"`
	ParentOnly         bool              `json:"parent_only"`
	WrapperIntent      bool              `json:"wrapper_intent"`
	Context            Subject           `json:"context"`
	Binding            R1Binding         `json:"binding"`
	Ready              *R1Evidence       `json:"ready,omitempty"`
	Failure            *R1Evidence       `json:"failure,omitempty"`
	Members            []Process         `json:"members,omitempty"`
	Main               *Process          `json:"main,omitempty"`
	Child              *Process          `json:"child,omitempty"`
	Endpoints          *R1EntryEndpoints `json:"endpoints,omitempty"`
	Timings            *R1EntryTimings   `json:"timings,omitempty"`
	ReceiptNS          int64             `json:"receipt_ns"`
	ObservationNS      int64             `json:"observation_ns"`
	MarginNS           int64             `json:"margin_ns"`
	Terminal           bool              `json:"terminal"`
	Natural            bool              `json:"natural"`
	ExpectedExit       bool              `json:"expected_exit"`
	OutputSHA256       string            `json:"output_sha256,omitempty"`
}

type R1EntryTimings struct {
	MembersNS   int64 `json:"members_ns"`
	HandlesNS   int64 `json:"handles_ns"`
	EndpointsNS int64 `json:"endpoints_ns"`
	RecheckNS   int64 `json:"recheck_ns"`
}

type R1EntryAncestor struct {
	Boundary Boundary `json:"boundary"`
	Max      string   `json:"max"`
	Current  uint64   `json:"current"`
}

type R1EntryHost struct {
	Kernel      string            `json:"kernel"`
	Client      string            `json:"client"`
	Manager     string            `json:"manager"`
	AvailableKB uint64            `json:"available_kb"`
	Ancestors   []R1EntryAncestor `json:"ancestors"`
}

func isR1Entry(a *Attempt) bool { return a.Config.Case == R1EntryCase }
func profileFor(c HostConfig) string {
	if c.Case == R1EntryCase {
		return R1EntryProfile
	}
	return HostProfile
}
func objectID(i FileIdentity) R1ObjectIdentity { return R1ObjectIdentity{i.Device, i.Inode} }

func entryBubblewrapArgs(path string) []string {
	return []string{"--unshare-all", "--unshare-user", "--uid", "0", "--gid", "0", "--die-with-parent", "--new-session", "--clearenv", "--cap-drop", "ALL",
		"--disable-userns", "--assert-userns-disabled", "--seccomp", strconv.Itoa(BPFFD), "--tmpfs", "/", "--dir", "/dev", "--dir", "/tmp",
		"--ro-bind", filepath.Join(path, "probe"), "/probe", "--ro-bind", filepath.Join(path, "r1-control"), R1ControlTarget,
		"--dev-bind", "/dev/null", "/dev/null", "--proc", "/proc", "--remount-ro", "/", "--chdir", "/", "--", "/probe", "--r1-memory"}
}

// Metadata only: never open the device for I/O, and never mutate its policy.
func entryNull() (R1NullDevice, error) {
	for _, path := range []string{"/", "/dev"} {
		var f *os.File
		var err error
		if path == "/" {
			f, err = os.Open("/")
		} else {
			f, err = openAbsolute(path, syscall.O_RDONLY|syscall.O_DIRECTORY)
		}
		if err != nil {
			return R1NullDevice{}, err
		}
		fi, err := f.Stat()
		f.Close()
		if err != nil || identity(fi).UID != 0 || fi.Mode().Perm()&0022 != 0 {
			return R1NullDevice{}, errors.New("untrusted null parent")
		}
	}
	f, err := openAbsolute("/dev/null", 0x200000) // O_PATH; O_NOFOLLOW is added by openAbsolute.
	if err != nil {
		return R1NullDevice{}, err
	}
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return R1NullDevice{}, err
	}
	n := R1NullDevice{FileIdentity{Device: st.Dev, Inode: st.Ino, UID: st.Uid}, st.Mode, st.Gid, st.Rdev}
	if !validEntryNull(n) {
		return n, errors.New("exact trusted null device required")
	}
	return n, nil
}
func validEntryNull(n R1NullDevice) bool {
	return n.Identity.Device != 0 && n.Identity.Inode != 0 && n.Identity.UID == 0 && n.GID == 0 && n.Mode == syscall.S_IFCHR|0666 && n.Rdev == 259
}

func prepareEntryStatic(a *Attempt, path string) error {
	if err := os.Mkdir(filepath.Join(path, "r1-control"), 0700); err != nil {
		return err
	}
	d, id, err := ownedDir(filepath.Join(path, "r1-control"))
	if err != nil {
		return err
	}
	defer d.Close()
	n, err := entryNull()
	if err != nil {
		return err
	}
	r := R1Request{1, a.Config.RunID, a.Config.RequestID, a.ID, R1Profile, a.FixtureNonce}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	managers := map[string]ELFMetadata{}
	for _, name := range []string{"systemctl", "systemd-run", "busctl"} {
		s, data, err := subject(a.Subjects[name].Path, true, false, false, 64<<20)
		if err != nil || s != a.Subjects[name] {
			return errors.New("manager subject changed before plan")
		}
		e, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return err
		}
		meta, err := managerELF(e)
		if err != nil {
			return err
		}
		managers[name] = meta
	}
	a.EntryStatic = &R1EntryStatic{R1EntryProfile, r, b, syntheticDigest(b), id, n, managers}
	return d.Sync()
}

func validateEntryStatic(a *Attempt, path string) error {
	if !isR1Entry(a) {
		if a.EntryStatic != nil || a.EntryRuntime != nil || a.EntryIntegrityFailure != "" {
			return errors.New("entry fields on legacy attempt")
		}
		return nil
	}
	s := a.EntryStatic
	if a.Config.validate() != nil || a.Profile != R1EntryProfile || a.DescriptorPolicy != FinalDescriptorPolicy || s == nil || s.Profile != R1EntryProfile || !validEntryNull(s.Null) || s.Control.Inode == 0 || s.Control.UID != a.Root.UID {
		return errors.New("invalid immutable entry profile")
	}
	if len(s.ManagerELF) != 3 {
		return errors.New("entry manager ELF inventory missing")
	}
	for _, name := range []string{"systemctl", "systemd-run", "busctl"} {
		if _, ok := s.ManagerELF[name]; !ok {
			return errors.New("entry manager subject missing")
		}
	}
	r := R1Request{1, a.Config.RunID, a.Config.RequestID, a.ID, R1Profile, a.FixtureNonce}
	b, _ := json.Marshal(r)
	if !r.valid() || s.Request != r || !bytes.Equal(s.RequestBytes, b) || s.RequestSHA256 != syntheticDigest(b) || path != filepath.Join(a.Config.Campaign, a.ID) {
		return errors.New("invalid exact entry request")
	}
	return nil
}

func entryContext(a *Attempt) (R1Context, []byte) {
	s := a.EntryStatic
	c := R1Context{s.Request, s.RequestSHA256, a.PlanSHA256, objectID(s.Control)}
	b, _ := json.Marshal(c)
	return c, b
}

func validateEntryStored(a *Attempt) error {
	if !isR1Entry(a) {
		return validateEntryStatic(a, "")
	}
	// Initial intent has no plan yet and confers no dispatch authority.
	if a.PlanSHA256 == "" && a.EntryStatic == nil && a.EntryRuntime == nil {
		return nil
	}
	if err := validateEntryStatic(a, filepath.Join(a.Config.Campaign, a.ID)); err != nil {
		return err
	}
	switch a.Stage {
	case "", "entry-preflight", "context-publication-intent", "retained-parent-intent", "tool-start-intent", "wrapper-entry-intent-pressure-release-prohibited", "entry-readiness", "entry-natural-timeout", "entry-reconcile-parent":
	default:
		return errors.New("prohibited entry ledger stage")
	}
	if a.EntryIntegrityFailure != "" && a.EntryIntegrityFailure != entryIntegrityFailure {
		return errors.New("invalid entry integrity status")
	}
	r := a.EntryRuntime
	if r == nil {
		if a.Phase != "prepared" && a.Phase != "launch-unknown" && a.Phase != "cleanup-unknown" {
			return errors.New("missing entry context receipt")
		}
		return nil
	}
	if r.ToolDispatchIntent && !r.ParentStartIntent || r.ServiceStopIntent && a.Workload == nil || r.ServiceStopped && !r.ServiceStopIntent {
		return errors.New("invalid entry cleanup intent")
	}
	if r.FailureReason != "" && !validText(r.FailureReason, 256) {
		return errors.New("invalid entry failure reason")
	}
	if a.Empty && !r.ParentOnly && (!r.Terminal || !a.TargetExited || a.Workload == nil || a.Injector == nil || a.Target == nil) {
		return errors.New("entry empty proof lacks original termination")
	}
	if a.Phase == "released" && (!r.WrapperIntent || a.Injector == nil) {
		return errors.New("entry wrapper intent absent")
	}
	if r.ParentOnly && (r.ToolDispatchIntent || a.Workload != nil || a.Injector != nil || a.Target != nil || r.Ready != nil || r.Main != nil || r.WrapperIntent || a.PopulatedLive || a.TargetExited || a.Parent == nil || !a.Empty) {
		return errors.New("invalid prelaunch entry cleanup")
	}
	if a.Parent != nil {
		if err := validateEntryParent(a, a.Parent); err != nil {
			return err
		}
	}
	if r.Host != nil {
		if err := validateEntryHost(r.Host); err != nil {
			return err
		}
	}
	if r.WrapperIntent && r.Host == nil {
		return errors.New("entry host preconditions absent")
	}
	_, b := entryContext(a)
	want := R1Binding{a.EntryStatic.Request, a.EntryStatic.RequestSHA256, a.PlanSHA256, syntheticDigest(b)}
	if r.Binding != want || r.Context.Path != filepath.Join(a.Config.Campaign, a.ID, "r1-control", R1ContextName) || r.Context.SHA256 != want.ContextSHA256 || r.Context.Identity.Inode == 0 || r.Context.Identity.UID != a.Root.UID {
		return errors.New("invalid stored context receipt")
	}
	if (r.Main == nil) != (r.Child == nil) || r.Ready == nil && r.Main != nil {
		return errors.New("partial actor binding")
	}
	if r.Ready != nil {
		if r.Ready.R1Binding != want || r.Ready.Ready == nil || !r.Ready.valid() {
			return errors.New("invalid stored entry readiness")
		}
		if r.Main != nil {
			if err := validateEntryMembers(a, *r.Ready.Ready, r.Members, *r.Main, *r.Child); err != nil {
				return err
			}
		}
	}
	if r.Endpoints != nil {
		if r.Main == nil || r.MarginNS <= 0 || r.MarginNS > int64(R1ReleaseWait) || r.ReceiptNS <= 0 || r.ObservationNS < 0 || r.ObservationNS >= int64(R1ReleaseWait) || r.Timings == nil {
			return errors.New("invalid stored observation timing")
		}
		for _, n := range []int64{r.Timings.MembersNS, r.Timings.HandlesNS, r.Timings.EndpointsNS, r.Timings.RecheckNS} {
			if n < 0 || n > r.ObservationNS {
				return errors.New("invalid entry stage duration")
			}
		}
		if err := validateEntryEndpoints(a, *r.Main, *r.Child, *r.Endpoints); err != nil {
			return err
		}
	}
	if r.Failure != nil && (r.Ready == nil || expectedEntryTrace(a, *r.Ready, *r.Failure) != nil) {
		return errors.New("invalid stored expected timeout")
	}
	if r.ExpectedExit && (!r.Natural || !r.Terminal) {
		return errors.New("invalid expected service exit")
	}
	if r.OutputSHA256 != "" && !validDigest(r.OutputSHA256) {
		return errors.New("invalid frozen entry output hash")
	}
	return nil
}

// Only context and wrapper-entry publication exist in this controller. The
// latter is at the attempt root, never inside the actor's control directory.
func entryAction(a *Attempt, action string) error {
	if !isR1Entry(a) || a.EntryIntegrityFailure != "" {
		return errors.New("entry action fenced")
	}
	switch action {
	case "context":
		if a.Phase == "prepared" && a.EntryRuntime == nil {
			return nil
		}
	case "wrapper-entry":
		if a.Phase == "bound" && a.EntryRuntime != nil && entryAdmitted(a) && !a.EntryRuntime.WrapperIntent && a.Injector != nil {
			return nil
		}
	}
	return errors.New("entry action prohibited or already delivered")
}

func (c *Campaign) publishEntryContext(a *Attempt) error {
	if err := entryAction(a, "context"); err != nil {
		return err
	}
	if err := authenticateEntry(a, false); err != nil {
		c.failEntryIntegrity(a)
		return err
	}
	_, b := entryContext(a)
	path := filepath.Join(c.Path, a.ID, "r1-control")
	a.Stage = "context-publication-intent"
	if err := c.Save(a, "prepared", ""); err != nil {
		return err
	}
	if err := c.entryPublish(path, R1ContextName, b); err != nil {
		// Rename followed by a failed directory sync is ambiguous delivery.
		c.failEntryIntegrity(a)
		return c.unknown(a, "launch-unknown", err)
	}
	s, _, err := subject(filepath.Join(path, R1ContextName), false, false, true, R1MaxFrame)
	if err != nil || s.SHA256 != syntheticDigest(b) {
		c.failEntryIntegrity(a)
		return c.unknown(a, "launch-unknown", errors.New("context capture failed"))
	}
	a.EntryRuntime = &R1EntryRuntime{Context: s, Binding: R1Binding{a.EntryStatic.Request, a.EntryStatic.RequestSHA256, a.PlanSHA256, s.SHA256}}
	return c.Save(a, "prepared", "")
}

func authenticateEntry(a *Attempt, contextRequired bool) error {
	s := a.EntryStatic
	if s == nil {
		return errors.New("entry static binding absent")
	}
	path := filepath.Join(a.Config.Campaign, a.ID)
	d, id, err := ownedDir(filepath.Join(path, "r1-control"))
	if err != nil {
		return err
	}
	defer d.Close()
	if id != s.Control {
		return errors.New("entry control directory changed")
	}
	// Reject misplaced finish as well as both forbidden pressure control names.
	for _, p := range []string{"r1-control/release.json", "r1-control/finish.json", "finish.json"} {
		if _, err := os.Lstat(filepath.Join(path, p)); !os.IsNotExist(err) {
			return errors.New("forbidden entry pressure control exists or is inaccessible")
		}
	}
	wrapperPath := filepath.Join(path, "release.json")
	if a.EntryRuntime == nil || !a.EntryRuntime.WrapperIntent {
		if _, err := os.Lstat(wrapperPath); !os.IsNotExist(err) {
			return errors.New("unexpected wrapper release")
		}
	} else if _, err := os.Lstat(wrapperPath); !os.IsNotExist(err) {
		_, b, err := subject(wrapperPath, false, false, true, R1MaxFrame)
		if a.Injector == nil {
			return errors.New("wrapper release without injector")
		}
		want, _ := json.Marshal(release{1, a.ID, a.Injector.Invocation})
		if err != nil || !bytes.Equal(b, want) {
			return errors.New("wrapper release changed")
		}
	}
	request, b, err := subject(filepath.Join(path, "request.json"), false, false, true, R1MaxFrame)
	if err != nil || request.Identity != a.Streams["request"] || request.SHA256 != s.RequestSHA256 || !bytes.Equal(b, s.RequestBytes) {
		return errors.New("entry request replaced")
	}
	if contextRequired {
		if a.EntryRuntime == nil {
			return errors.New("entry context receipt absent")
		}
		got, b, err := subject(filepath.Join(path, "r1-control", R1ContextName), false, false, true, R1MaxFrame)
		_, want := entryContext(a)
		if err != nil || got != a.EntryRuntime.Context || !bytes.Equal(b, want) {
			return errors.New("entry context replaced")
		}
	} else if _, err := os.Lstat(filepath.Join(path, "r1-control", R1ContextName)); !os.IsNotExist(err) {
		return errors.New("unexpected entry context")
	}
	if r := a.EntryRuntime; r != nil && r.OutputSHA256 != "" {
		out, _, err := subject(filepath.Join(path, "output"), false, false, true, MaxOutputBytes)
		if err != nil || out.Identity != a.Streams["output"] || out.SHA256 != r.OutputSHA256 {
			return errors.New("frozen entry output changed")
		}
	}
	return nil
}

func (c *Campaign) failEntryIntegrity(a *Attempt) {
	a.EntryIntegrityFailure = entryIntegrityFailure
	c.Blocked = true
	// Carry observed integrity loss across reopen even if an attacker restores
	// the bytes before the next intent Save. Preserve the structurally validated
	// stop record; this marker never authorizes an effect.
	path := filepath.Join(a.Config.Campaign, a.ID)
	d, id, err := ownedDir(path)
	if err != nil || id != a.Root {
		if d != nil {
			d.Close()
		}
		c.poison(nil)
		return
	}
	defer d.Close()
	marker := filepath.Join(path, "entry-integrity-failed")
	if _, err := os.Lstat(marker); os.IsNotExist(err) {
		if err := exclusiveFile(marker, []byte(entryIntegrityFailure+"\n"), 0400); err != nil {
			c.poison(d)
		}
	} else if err != nil {
		c.poison(d)
	}
}
func (c *Campaign) checkEntryIntegrity(a *Attempt) {
	if !isR1Entry(a) || a.EntryStatic == nil || a.PlanSHA256 == "" {
		return
	}
	_, markerErr := os.Lstat(filepath.Join(a.Config.Campaign, a.ID, "entry-integrity-failed"))
	if a.EntryIntegrityFailure != "" || !os.IsNotExist(markerErr) || authenticateEntry(a, a.EntryRuntime != nil) != nil {
		c.failEntryIntegrity(a)
	}
}

func entryDispatchCheck(a *Attempt) error {
	if !isR1Entry(a) {
		return nil
	}
	if a.EntryIntegrityFailure != "" || authenticateEntry(a, true) != nil {
		return errors.New("entry dispatch integrity failed")
	}
	n, err := entryNull()
	if err != nil || n != a.EntryStatic.Null {
		return errors.New("entry dispatch null device changed")
	}
	return nil
}

func expectedEntryTrace(a *Attempt, ready, failure R1Evidence) error {
	if a.EntryRuntime == nil || !ready.valid() || !failure.valid() || ready.R1Binding != a.EntryRuntime.Binding || failure.R1Binding != ready.R1Binding || ready.Ready == nil || failure.Failure == nil || ready.Ready.ControlDirectory != objectID(a.EntryStatic.Control) {
		return errors.New("unauthenticated entry trace")
	}
	f := failure.Failure
	ch := f.Child
	if failure.Sequence != 1 || f.Stage != "release" || f.Error != "deadline" || f.ReleaseSHA256 != "" || f.FinishSHA256 != "" || f.TouchedBytes != 0 || f.PageSize != 0 || f.Cleanup != "not-allocated" || ch.Identity == nil || *ch.Identity != ready.Ready.Child || ch.Token != "abort" || ch.Termination != "exited" || ch.Reap != "reaped" || ch.ExitCode == nil || *ch.ExitCode != 20 || ch.Signal != nil {
		return errors.New("expected release-timeout/abort/reap absent")
	}
	return nil
}

func entryObserved(a *Attempt) bool {
	r := a.EntryRuntime
	return isR1Entry(a) && a.Phase == "complete" && a.Empty && a.TargetExited && a.PopulatedLive && a.SliceStopped && a.EntryIntegrityFailure == "" && r != nil && entryAdmitted(a) && r.FailureReason == "" && r.Host != nil && validateEntryHost(r.Host) == nil && r.Main != nil && r.Child != nil && r.Endpoints != nil && r.Ready != nil && r.Failure != nil && r.Terminal && r.Natural && r.ExpectedExit && validDigest(r.OutputSHA256) && expectedEntryTrace(a, *r.Ready, *r.Failure) == nil
}

// Older entry records may still carry the original dispatch label. Retain only
// unambiguous facts before reconciliation overwrites it. An already ambiguous
// cleanup label cannot establish an absent dispatch fact.
func retainEntryIntents(a *Attempt) {
	r := a.EntryRuntime
	if r == nil {
		return
	}
	if a.Stage == "retained-parent-intent" || a.Phase == "launching-slice" || a.Phase == "slice-bound" || a.Phase == "launching-service" || a.Workload != nil || r.ParentOnly || r.WrapperIntent || a.Stage == "tool-start-intent" {
		r.ParentStartIntent = true
	}
	if a.Stage == "tool-start-intent" || a.Phase == "launching-service" || a.Workload != nil || r.WrapperIntent {
		r.ToolDispatchIntent = true
	}
}

func validateEntryHost(h *R1EntryHost) error {
	if !validText(h.Kernel, 511) || !validText(h.Manager, 511) || !strings.HasPrefix(h.Client, "systemd ") || len(h.Client) > 4096 || h.AvailableKB < 512<<10 || len(h.Ancestors) == 0 || len(h.Ancestors) > 32 {
		return errors.New("entry host preconditions incomplete")
	}
	for j, a := range h.Ancestors {
		if a.Boundary.Identity.Inode == 0 || !strings.HasPrefix(a.Boundary.Path, "/") || filepath.Clean(a.Boundary.Path) != a.Boundary.Path || j > 0 && a.Boundary.Path != filepath.Dir(h.Ancestors[j-1].Boundary.Path) {
			return errors.New("entry ancestor identity invalid")
		}
		if !entryMemorySafe(a.Max, a.Current, true) {
			return errors.New("entry ancestor headroom unsafe")
		}
	}
	return nil
}

func entryParentOnly(a *Attempt) bool {
	return isR1Entry(a) && a.EntryRuntime != nil && a.EntryRuntime.ParentOnly
}

func entryNow(h hostIO) time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// Record the exact namespace projection, rejecting duplicate/malformed NSpid.
func entryNSPID(p Process) (int, error) {
	var ids []string
	for _, line := range strings.Split(p.Status, "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			if ids != nil {
				return 0, errors.New("duplicate NSpid")
			}
			ids = strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		}
	}
	if len(ids) != 2 {
		return 0, errors.New("exact host/child namespace projection required")
	}
	seen := map[int]bool{}
	for j, text := range ids {
		n, err := strconv.Atoi(text)
		if err != nil || n <= 0 || strconv.Itoa(n) != text || seen[n] {
			return 0, errors.New("invalid NSpid")
		}
		seen[n] = true
		if j == 0 && n != p.PID {
			return 0, errors.New("host NSpid mismatch")
		}
	}
	return strconv.Atoi(ids[1])
}

func entryActorMatches(a *Attempt, p Process, id R1ActorIdentity) bool {
	ns, err := entryNSPID(p)
	return err == nil && validProcess(p) && p.Boot == a.Boot && ns == id.PID && p.Start == id.Start && objectID(p.Executable) == id.Executable && p.Executable == a.Snapshots["probe"].Identity && objectID(p.Namespaces["pid"]) == id.PIDNamespace && p.Cgroup == a.Workload.Boundary.Path
}

func entryPPID(p Process) (int, error) {
	text := ""
	for _, line := range strings.Split(p.Status, "\n") {
		if strings.HasPrefix(line, "PPid:") {
			if text != "" {
				return 0, errors.New("duplicate PPid")
			}
			text = strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, errors.New("missing parent identity")
	}
	return n, nil
}

func validateEntryMembers(a *Attempt, ready R1Ready, members []Process, main, child Process) error {
	if a.Workload == nil || a.Injector == nil || main.PID == child.PID || !entryActorMatches(a, main, ready.Main) || !entryActorMatches(a, child, ready.Child) || !reflect.DeepEqual(main.Namespaces, child.Namespaces) || len(members) < 3 || len(members) > 4 {
		return errors.New("entry actor pair or membership mismatch")
	}
	seen := map[int]Process{}
	for _, p := range members {
		if _, ok := seen[p.PID]; ok || !validProcess(p) || p.Boot != a.Boot || p.Cgroup != a.Workload.Boundary.Path {
			return errors.New("duplicate or escaped entry member")
		}
		if p.PID != main.PID && p.PID != child.PID && p.Executable != a.Snapshots["bwrap"].Identity {
			return errors.New("unknown entry member")
		}
		seen[p.PID] = p
	}
	if !sameProcess(seen[main.PID], main) || !sameProcess(seen[child.PID], child) {
		return errors.New("entry actors missing from exact membership")
	}
	monitor, ok := seen[a.Injector.Process.PID]
	if !ok || monitor.Start != a.Injector.Process.Start || monitor.Executable != a.Snapshots["bwrap"].Identity {
		return errors.New("retained injector exec transition unproved")
	}
	parent, err := entryPPID(child)
	if err != nil || parent != main.PID {
		return errors.New("child parent mismatch")
	}
	// Every wrapper must be on the main actor's single ancestry chain. A
	// matching bwrap executable in a sibling branch is still unexplained.
	wrappers := map[int]bool{}
	cur := main
	for hops := 0; ; hops++ {
		pid, err := entryPPID(cur)
		if err != nil || hops >= 2 || wrappers[pid] {
			return errors.New("unknown member branch")
		}
		up, ok := seen[pid]
		if !ok || up.Executable != a.Snapshots["bwrap"].Identity {
			return errors.New("unbound member ancestry")
		}
		wrappers[pid] = true
		if pid == monitor.PID {
			break
		}
		cur = up
	}
	if len(wrappers) != len(members)-2 {
		return errors.New("unknown bwrap sibling branch")
	}
	for _, p := range []Process{main, child} {
		for _, n := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
			if p.Namespaces[n] == a.Injector.Process.Namespaces[n] {
				return errors.New("entry shared namespace")
			}
		}
		for _, line := range []string{"NoNewPrivs:\t1", "Seccomp:\t2", "CapInh:\t0000000000000000", "CapAmb:\t0000000000000000", "CapEff:\t0000000000000000", "CapPrm:\t0000000000000000", "CapBnd:\t0000000000000000"} {
			if !strings.Contains(p.Status, line+"\n") {
				return errors.New("entry privilege state unproved")
			}
		}
		if !readOnlyMount(p.Mounts.Root) || !readOnlyMount(p.Mounts.Probe) {
			return errors.New("entry root/probe writable")
		}
	}
	return nil
}
