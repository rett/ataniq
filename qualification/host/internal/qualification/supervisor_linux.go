//go:build linux && amd64

package qualification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// hostIO has narrow deterministic test seams, not a backend interface/registry.
// Unit tests install transcripts and never call liveHost.
type hostIO struct {
	call           func(string, []string) (string, error)
	boundary       func(string) (Boundary, error)
	population     func(Boundary) (bool, error)
	process        func(int) (Process, error)
	ended          func(Process) (bool, error)
	handle         func(Process) (*os.File, error)
	exit           func(*os.File) (bool, error)
	helper         func(Boundary, FileIdentity) (Process, error)
	budget         func(*Unit) error
	pause          func()
	now            func() time.Time
	entryMembers   func(Boundary) ([]Process, error)
	entryEndpoints func(Process, Process) (R1EntryEndpoints, error)
	entrySafety    func(*Attempt, *Unit) error
	entryNull      func() (R1NullDevice, error)
	entryTree      func(Boundary, Boundary) error
	deadlineCall   func(string, []string, time.Time) (string, error)
	entryUntil     time.Time
}

type cappedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxOutputBytes {
		b.overflow = true
		return 0, errors.New("supervisor output cap")
	}
	return b.Buffer.Write(p)
}

func liveHost() hostIO {
	return hostIO{call: func(path string, args []string) (string, error) {
		return hostCall(path, args, time.Now().Add(5*time.Second))
	}, boundary: boundary, population: populated, process: process, ended: processEnded, handle: processHandle, exit: exited, helper: helperIn, pause: func() { time.Sleep(10 * time.Millisecond) }, now: time.Now, entryMembers: entryMembers, entryEndpoints: entryEndpoints, entrySafety: entrySafety, entryNull: entryNull, entryTree: entryTree, deadlineCall: hostCall}
}

func hostCall(path string, args []string, deadline time.Time) (string, error) {
	if cap := time.Now().Add(5 * time.Second); cap.Before(deadline) {
		deadline = cap
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	// Host manager connection is trusted controller authority; never forwarded
	// to the workload. No shell, PATH search, listener or manager reconfiguration.
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "HOME=" + os.Getenv("HOME"), "XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR"), "DBUS_SESSION_BUS_ADDRESS=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS")}
	cmd.WaitDelay = time.Second
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if out.overflow {
		return "", errors.New("supervisor output excessive")
	}
	return out.String(), err
}

const properties = "Id,LoadState,ActiveState,SubState,InvocationID,ControlGroup,MainPID,Type,Slice,Description,ExecStart,StopWhenUnneeded,KillMode,NoNewPrivileges,RuntimeMaxUSec,TimeoutStopUSec,MemoryMax,TasksMax,CPUQuotaPerSecUSec,Transient,StandardInput,StandardOutput,StandardError,LimitNOFILE,LimitFSIZE,Restart,RemainAfterExit,Job,FragmentPath"
const parentProperties = "Id,LoadState,ActiveState,SubState,InvocationID,ControlGroup,StopWhenUnneeded,Job"
const freshProperties = "Id,LoadState,ActiveState,ControlGroup,FragmentPath,Job"

func noJob(p map[string]string) bool { v, ok := p["Job"]; return ok && (v == "" || v == "0") }

func (h hostIO) freshNames(a *Attempt) error {
	for _, name := range []string{a.Service, a.Slice} {
		b, err := h.ctl(a, "show", "--all", "--property="+freshProperties, "--", name)
		if err != nil {
			return err
		}
		p, err := parseProperties(b, freshProperties)
		if err != nil {
			return err
		}
		loadOK := p["LoadState"] == "not-found" || name == a.Slice && p["LoadState"] == "loaded"
		if p["Id"] != name || !loadOK || p["ActiveState"] != "inactive" || p["ControlGroup"] != "" || p["FragmentPath"] != "" || !noJob(p) {
			return errors.New("fresh exact names unavailable")
		}
		if name == a.Service {
			a.FreshService = p
		} else {
			a.FreshSlice = p
		}
	}
	return nil
}

func parseProperties(s, required string) (map[string]string, error) {
	if len(s) > MaxOutputBytes {
		return nil, errors.New("show output excessive")
	}
	p := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("malformed show output")
		}
		if _, ok = p[key]; ok {
			return nil, errors.New("duplicate show property")
		}
		if !strings.Contains(","+required+",", ","+key+",") {
			return nil, errors.New("unexpected show property")
		}
		p[key] = value
	}
	for _, key := range strings.Split(required, ",") {
		if _, ok := p[key]; !ok {
			return nil, errors.New("missing show property")
		}
	}
	return p, nil
}

func (h hostIO) ctl(a *Attempt, args ...string) (string, error) {
	s, _, err := subject(a.Config.Systemctl, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["systemctl"] {
		return "", errors.New("systemctl subject changed")
	}
	return h.call(s.Path, append([]string{"--user", "--no-pager"}, args...))
}

func (h hostIO) query(a *Attempt, name string) (*Unit, error) {
	required := properties
	if isR1Entry(a) {
		required = entryServiceProperties
	}
	if name == a.Slice {
		required = parentProperties
		if isR1Entry(a) {
			required = entryParentProperties
		}
	}
	s, err := h.ctl(a, "show", "--all", "--property="+required, "--", name)
	if err != nil {
		return nil, err
	}
	p, err := parseProperties(s, required)
	if err != nil {
		return nil, err
	}
	if p["Id"] != name || p["LoadState"] != "loaded" || p["ActiveState"] != "active" || !noJob(p) {
		return nil, errors.New("unit missing or inactive; emptiness unproved")
	}
	b, err := h.boundary(p["ControlGroup"])
	if err != nil {
		return nil, err
	}
	return &Unit{Properties: p, Boundary: b}, nil
}

func (h hostIO) parent(a *Attempt) (*Unit, error) {
	u, err := h.query(a, a.Slice)
	if err != nil {
		return nil, err
	}
	if filepath.Base(u.Boundary.Path) != a.Slice || u.Properties["StopWhenUnneeded"] != "no" || !bounded(u.Properties["InvocationID"], 32, 32, isHex) {
		return nil, errors.New("unsupported: retained parent slice unavailable")
	}
	if a.Parent != nil && !sameUnit(a.Parent, u) {
		return nil, errors.New("parent identity mismatch")
	}
	if isR1Entry(a) {
		if err := validateEntryParent(a, u); err != nil {
			return nil, err
		}
		if err := h.entryEffective(u, true); err != nil {
			return nil, err
		}
	}
	self, err := h.process(os.Getpid())
	if err != nil {
		return nil, err
	}
	if self.Boot != a.Boot {
		return nil, errors.New("host boot identity changed")
	}
	if self.Cgroup == u.Boundary.Path || strings.HasPrefix(self.Cgroup, u.Boundary.Path+"/") {
		return nil, errors.New("controller inside constrained boundary")
	}
	return u, nil
}

func sameUnit(a, b *Unit) bool {
	return a.Boundary == b.Boundary && a.Properties["Id"] == b.Properties["Id"] && a.Properties["InvocationID"] == b.Properties["InvocationID"]
}

func (h hostIO) service(a *Attempt) (*Unit, error) {
	u, err := h.query(a, a.Service)
	if err != nil {
		return nil, err
	}
	p := u.Properties
	if a.Parent == nil || u.Boundary.Path != a.Parent.Boundary.Path+"/"+a.Service || p["Slice"] != a.Slice || !bounded(p["InvocationID"], 32, 32, isHex) || p["Description"] != "ataniq-qualification-"+a.ID {
		return nil, errors.New("service ownership mismatch")
	}
	for key, want := range map[string]string{"Type": "exec", "Transient": "yes", "Restart": "no", "RemainAfterExit": "no", "KillMode": "control-group", "NoNewPrivileges": "yes", "RuntimeMaxUSec": "20s", "TimeoutStopUSec": "3s", "MemoryMax": "67108864", "TasksMax": "32", "CPUQuotaPerSecUSec": "1s", "LimitNOFILE": "1024", "LimitFSIZE": "131072", "StandardInput": "file", "StandardOutput": "append", "StandardError": "append"} {
		if isR1Entry(a) {
			if key == "RuntimeMaxUSec" {
				want = "30s"
			}
			if key == "MemoryMax" {
				want = "100663296"
			}
		}
		if p[key] != want {
			return nil, fmt.Errorf("unverified safety property %s", key)
		}
	}
	if isR1Entry(a) && (p["MemorySwapMax"] != "0" || p["Delegate"] != "no") {
		return nil, errors.New("entry swap/delegation unverified")
	}
	if !commandMatches(a, p["ExecStart"]) {
		return nil, errors.New("service command mismatch")
	}
	pid, err := strconv.Atoi(p["MainPID"])
	if err != nil {
		return nil, errors.New("missing service main pid")
	}
	main, err := h.process(pid)
	if err != nil {
		return nil, err
	}
	u.Main = &main
	if u.Main.Cgroup != u.Boundary.Path {
		return nil, errors.New("main pid outside service")
	}
	if a.Workload != nil && (u.Boundary != a.Workload.Boundary || p["InvocationID"] != a.Workload.Properties["InvocationID"]) {
		return nil, errors.New("service invocation or cgroup mismatch")
	}
	return u, nil
}

// ExecStart also carries changing process/exit timestamps. Match the declared
// command, not those volatile fields, against the durable launch plan.
func commandMatches(a *Attempt, observed string) bool {
	path := filepath.Join(a.Config.Campaign, a.ID)
	expected := "path=" + filepath.Join(path, "driver") + " ; argv[]=" + filepath.Join(path, "driver") + " private-inject " + path + " ;"
	return strings.Count(observed, "{") == 1 && strings.Contains(observed, expected)
}

// Limits are safety budgets, not an assertion that resource pressure was tested.
func (h hostIO) effective(u *Unit) error {
	if h.budget != nil {
		return h.budget(u)
	}
	for key, want := range map[string]string{"memory.max": "67108864", "pids.max": "32", "cpu.max": "100000 100000"} {
		b, err := kernelRead("/sys/fs/cgroup"+u.Boundary.Path+"/"+key, 4096)
		if err != nil || strings.TrimSpace(string(b)) != want {
			return errors.New("effective aggregate safety budget unverified")
		}
		u.Properties["effective_"+strings.ReplaceAll(key, ".", "_")] = strings.TrimSpace(string(b))
	}
	return nil
}

// Reconcile never launches or releases an injector. Adoption is cleanup-only.
func (c *Campaign) reconcile(a *Attempt, h hostIO) error {
	if isR1Entry(a) {
		return c.reconcileEntry(a, h)
	}
	if a.Phase == "complete" {
		if a.FD1IntegrityFailure != "" {
			return c.Save(a, "complete", a.Reason)
		}
		return nil
	}
	a.Stage = "reconcile-parent"
	parent, err := h.parent(a)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	// A retained live binding and the same empty parent can prove cleanup
	// after exec, process exit and service GC. This produces no case verdict.
	if a.Parent != nil && a.Workload != nil && a.Injector != nil && a.Target != nil && a.PopulatedLive {
		pop, e := h.population(parent.Boundary)
		if e != nil {
			return c.unknown(a, "cleanup-unknown", e)
		}
		if !pop {
			return c.recoverEmpty(a, h)
		}
	}
	if a.Parent == nil {
		a.Parent = parent
	}
	a.Stage = "reconcile-service"
	u, err := h.service(a)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if a.Injector == nil {
		i, err := readInjection(filepath.Join(c.Path, a.ID, "injection.json"))
		if err != nil || validateInjection(a, i, u) != nil {
			return c.unknown(a, "cleanup-unknown", errors.New("cannot bind exact service injector"))
		}
		if err = authenticateFD1Runtime(a, i.FD1Runtime); err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		a.FD1Runtime = i.FD1Runtime
		a.Injector = &i
		a.Workload = u
		p := i.Process
		a.Target = &p
	}
	if a.Injector.Invocation != u.Properties["InvocationID"] {
		return c.unknown(a, "cleanup-unknown", errors.New("wrong invocation"))
	}
	// Preserve original injector/target identities across exec and recovery.
	if a.Target == nil {
		return c.unknown(a, "cleanup-unknown", errors.New("original target binding absent"))
	}
	p, err := h.process(a.Target.PID)
	if err != nil || p.Boot != a.Target.Boot || p.PID != a.Target.PID || p.Start != a.Target.Start || p.Cgroup != a.Target.Cgroup {
		return c.unknown(a, "cleanup-unknown", errors.New("owned live target unavailable"))
	}
	fd, err := h.handle(p)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	defer fd.Close()
	if a.Ready != nil && a.Target != nil && !sameProcess(*a.Target, p) {
		return c.unknown(a, "cleanup-unknown", errors.New("retained helper identity changed"))
	}
	live, err := h.population(parent.Boundary)
	if err != nil || !live {
		return c.unknown(a, "cleanup-unknown", errors.New("retained live population unproved"))
	}
	a.PopulatedLive = true
	return c.cancel(a, h, fd)
}

// Missing service alone never establishes termination. Require no pending job
// and either exact not-found evidence or the bound, non-restarting terminal unit.
func (h hostIO) terminal(a *Attempt) error {
	b, err := h.ctl(a, "show", "--all", "--property="+freshProperties, "--", a.Service)
	if err != nil {
		return err
	}
	p, err := parseProperties(b, freshProperties)
	if err != nil {
		return err
	}
	if p["Id"] != a.Service || !noJob(p) {
		return errors.New("terminal service identity or pending job")
	}
	if p["LoadState"] == "not-found" && p["ActiveState"] == "inactive" && p["ControlGroup"] == "" && p["FragmentPath"] == "" {
		return nil
	}
	if p["LoadState"] != "loaded" || p["ActiveState"] != "inactive" && p["ActiveState"] != "failed" {
		return errors.New("service not terminal")
	}
	b, err = h.ctl(a, "show", "--all", "--property="+properties, "--", a.Service)
	if err != nil {
		return err
	}
	p, err = parseProperties(b, properties)
	if err != nil {
		return err
	}
	old := a.Workload.Properties
	for _, k := range []string{"Id", "InvocationID", "Description", "Slice", "Type", "Restart", "RemainAfterExit"} {
		if p[k] != old[k] {
			return errors.New("terminal service binding changed")
		}
	}
	if !commandMatches(a, p["ExecStart"]) {
		return errors.New("terminal command mismatch")
	}
	if !noJob(p) || p["Restart"] != "no" || p["MainPID"] != "0" || p["ActiveState"] != "inactive" && p["ActiveState"] != "failed" || p["ControlGroup"] != "" && p["ControlGroup"] != a.Workload.Boundary.Path {
		return errors.New("terminal service state unproved")
	}
	return nil
}

func (c *Campaign) recoverEmpty(a *Attempt, h hostIO) error {
	a.Stage = "reconcile-empty"
	if h.ended == nil {
		return c.unknown(a, "cleanup-unknown", errors.New("recovery process evidence unavailable"))
	}
	for _, p := range []Process{a.Injector.Process, *a.Target} {
		dead, err := h.ended(p)
		if err != nil || !dead {
			return c.unknown(a, "cleanup-unknown", errors.New("original process exit unproved"))
		}
	}
	if err := h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if _, err := h.parent(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	pop, err := h.population(a.Parent.Boundary)
	if err != nil || pop {
		return c.unknown(a, "cleanup-unknown", errors.New("same retained parent empty unproved"))
	}
	// Requery after the empty sample: no queued start/restart may be ignored.
	if err := h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	a.TargetExited = true
	a.Empty = true
	if err := c.Save(a, "empty", ""); err != nil {
		return err
	}
	if _, err := h.ctl(a, "stop", "--", a.Slice); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	a.SliceStopped = true
	return c.Save(a, "complete", "")
}

func (c *Campaign) unknown(a *Attempt, phase string, cause error) error {
	c.Blocked = true
	a.ReasonCode = "identity-or-evidence-unproved"
	if err := c.Save(a, phase, hostReason(cause)); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func hostReason(err error) string {
	if err != nil {
		s := strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, err.Error())
		if len(s) > 240 {
			s = string([]rune(s)[:min(120, len([]rune(s)))])
		}
		if validText(s, 256) {
			return s
		}
	}
	return "exact identity or cleanup unproved"
}

func (c *Campaign) cancel(a *Attempt, h hostIO, target *os.File) error {
	a.Stage = "cancel"
	if a.Parent == nil || a.Workload == nil || a.Target == nil || !a.PopulatedLive {
		return c.unknown(a, "cleanup-unknown", errors.New("cancellation prerequisites absent"))
	}
	parent, err := h.parent(a)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	u, err := h.service(a)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if u.Properties["InvocationID"] != a.Workload.Properties["InvocationID"] || u.Boundary != a.Workload.Boundary {
		return c.unknown(a, "cleanup-unknown", errors.New("cancel identity mismatch"))
	}
	// The held process may already have exited in a finite fast case. Its pidfd
	// remains authoritative; hold cancellation separately requires it alive.
	if err = c.Save(a, "cancelling", ""); err != nil {
		return err
	}
	if a.Ready != nil {
		actual, e := h.process(a.Target.PID)
		dead, pe := h.exit(target)
		if e != nil || pe != nil || dead || !sameProcess(*a.Target, actual) {
			return c.unknown(a, "cleanup-unknown", errors.New("live cancellation target lost"))
		}
	}
	if _, err = h.ctl(a, "stop", "--", a.Service); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	for n := 0; n < 300; n++ {
		dead, e := h.exit(target)
		if e != nil {
			return c.unknown(a, "cleanup-unknown", e)
		}
		pop, e := h.population(parent.Boundary)
		if e != nil {
			return c.unknown(a, "cleanup-unknown", e)
		}
		if dead && !pop {
			a.TargetExited = true
			a.Empty = true
			break
		}
		h.pause()
	}
	if !a.Empty {
		return c.unknown(a, "cleanup-unknown", errors.New("complete boundary empty not proved"))
	}
	if err = h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	// Recheck the live retained unit, after post-workload populated=0, before stop.
	if _, err = h.parent(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if err = c.Save(a, "empty", ""); err != nil {
		return err
	}
	if _, err = h.ctl(a, "stop", "--", a.Slice); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	a.SliceStopped = true
	return c.Save(a, "complete", "")
}
