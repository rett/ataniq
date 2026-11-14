//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const entryServiceProperties = properties + ",MemorySwapMax,MemoryHigh,OOMPolicy,Delegate,Result,ExecMainCode,ExecMainStatus"
const entryParentProperties = parentProperties + ",Description,Transient,MemoryMax,MemorySwapMax,MemoryHigh,TasksMax,CPUQuotaPerSecUSec"

func entryParentArgs(a *Attempt) []string {
	// Public user-manager API; exact typed properties are immutable controller
	// code. No unit files, drop-ins, policy updates or arbitrary bus operations.
	return []string{"--user", "--timeout=5s", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))",
		a.Slice, "fail", "8", "Description", "s", "ataniq-entry-" + a.ID, "StopWhenUnneeded", "b", "false", "MemoryMax", "t", "268435456", "MemorySwapMax", "t", "0", "MemoryHigh", "t", "18446744073709551615", "TasksMax", "t", "64", "CPUQuotaPerSecUSec", "t", "1000000", "DefaultDependencies", "b", "true", "0"}
}

func (h hostIO) startEntryParent(a *Attempt) error {
	s, _, err := subject(a.Config.Busctl, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["busctl"] {
		return errors.New("busctl subject changed")
	}
	result, err := h.call(s.Path, entryParentArgs(a))
	if err != nil {
		return err
	}
	// Acknowledgement identifies only delivery. Ownership still requires exact
	// unit generation/cgroup and direct effective limits before Tool dispatch.
	f := strings.Fields(result)
	if len(f) != 2 || f[0] != "o" || !strings.HasPrefix(f[1], `"/org/freedesktop/systemd1/job/`) || !strings.HasSuffix(f[1], `"`) {
		return errors.New("parent start acknowledgement unknown")
	}
	n := strings.TrimSuffix(strings.TrimPrefix(f[1], `"/org/freedesktop/systemd1/job/`), `"`)
	if _, err := strconv.ParseUint(n, 10, 64); err != nil {
		return errors.New("invalid parent job identity")
	}
	return nil
}

func (h hostIO) entryEffective(u *Unit, parent bool) error {
	if h.budget != nil {
		return h.budget(u)
	}
	wants := map[string]string{"memory.max": "100663296", "memory.swap.max": "0", "memory.high": "max", "memory.oom.group": "1", "pids.max": "32", "cpu.max": "100000 100000"}
	if parent {
		wants["memory.max"], wants["pids.max"] = "268435456", "64"
		delete(wants, "memory.oom.group") // OOMPolicy is service-only.
	}
	read := h.entryRead
	if read == nil {
		read = entryKernel
	}
	before, err := h.boundary(u.Boundary.Path)
	if err != nil || before != u.Boundary {
		return errors.New("entry budget boundary replaced")
	}
	for key, want := range wants {
		b, err := read(u.Boundary.Path, key)
		if err != nil || strings.TrimSpace(string(b)) != want {
			return fmt.Errorf("entry effective %s unverified", key)
		}
		u.Properties["effective_"+strings.ReplaceAll(key, ".", "_")] = want
	}
	after, err := h.boundary(u.Boundary.Path)
	if err != nil || after != u.Boundary {
		return errors.New("entry budget boundary changed")
	}
	return nil
}

func validateEntryParent(a *Attempt, u *Unit) error {
	if u.Properties["Description"] != "ataniq-entry-"+a.ID {
		return errors.New("entry parent description mismatch")
	}
	for k, want := range map[string]string{"Transient": "yes", "MemoryMax": "268435456", "MemorySwapMax": "0", "MemoryHigh": "infinity", "TasksMax": "64", "CPUQuotaPerSecUSec": "1s"} {
		if u.Properties[k] != want {
			return errors.New("entry parent safety property unverified")
		}
	}
	root := filepath.Dir(u.Boundary.Path)
	if !strings.HasSuffix(root, "/user@"+strconv.Itoa(os.Getuid())+".service") || strings.Contains(root, "/app.slice/") {
		return errors.New("entry parent is outside intended user-manager root")
	}
	return nil
}

// Actual conservative headroom is a precondition, never inferred from limits.
// This read-only preflight cannot reserve future memory; the guards bound the
// entry experiment under the trusted host/administrator premise.
func entrySafety(a *Attempt, parent *Unit) error {
	if parent != nil {
		return nil
	} // Direct parent budgets are checked separately.
	self, err := process(os.Getpid())
	if err != nil {
		return err
	}
	root := self.Cgroup
	for root != "/" && !strings.HasSuffix(root, "/user@"+strconv.Itoa(os.Getuid())+".service") {
		root = filepath.Dir(root)
	}
	if root == "/" {
		return errors.New("unsupported: intended user-manager ancestry unavailable")
	}
	host := &R1EntryHost{Ancestors: []R1EntryAncestor{}}
	kernel, err := kernelRead("/proc/sys/kernel/osrelease", 512)
	if err != nil {
		return err
	}
	host.Kernel = strings.TrimSpace(string(kernel))
	if !validText(host.Kernel, 511) {
		return errors.New("kernel version unavailable")
	}
	for path := root; ; path = filepath.Dir(path) {
		max, err := entryKernel(path, "memory.max")
		// The cgroup root has no memory controller max/current files.
		if path == "/" {
			break
		}
		if err != nil {
			return err
		}
		current, err := entryKernel(path, "memory.current")
		if err != nil {
			return err
		}
		used, err := strconv.ParseUint(strings.TrimSpace(string(current)), 10, 64)
		if err != nil {
			return err
		}
		if text := strings.TrimSpace(string(max)); text != "max" {
			limit, err := strconv.ParseUint(text, 10, 64)
			if err != nil || used > limit || limit-used < 384<<20 {
				return errors.New("unsupported: insufficient or unknown ancestor headroom")
			}
		}
		bound, err := boundary(path)
		if err != nil {
			return err
		}
		host.Ancestors = append(host.Ancestors, R1EntryAncestor{bound, strings.TrimSpace(string(max)), used})
	}
	b, err := kernelRead("/proc/meminfo", 8192)
	if err != nil {
		return err
	}
	available := uint64(0)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			if available != 0 {
				return errors.New("ambiguous host headroom")
			}
			available, err = strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return err
			}
		}
	}
	if available < 512<<10 {
		return errors.New("unsupported: insufficient or unknown global headroom")
	}
	host.AvailableKB = available
	a.EntryRuntime.Host = host
	return nil
}

func (h hostIO) entryPreflight(a *Attempt) error {
	if h.entrySafety == nil || h.entryMembers == nil || h.entryEndpoints == nil || h.entryNull == nil || h.entryTree == nil {
		return errors.New("entry observers unavailable")
	}
	if err := h.entrySafety(a, nil); err != nil {
		return err
	}
	// No automatic oomd/policy adjustment. Unknown or active system oomd is
	// unsupported for this fixed safety envelope.
	s, _, err := subject(a.Config.Systemctl, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["systemctl"] {
		return errors.New("systemctl changed")
	}
	version, err := h.call(s.Path, []string{"--version"})
	if err != nil || a.EntryRuntime.Host == nil || len(version) > 4096 || !strings.HasPrefix(version, "systemd ") {
		return errors.New("manager version unavailable")
	}
	a.EntryRuntime.Host.Client = strings.TrimSpace(version)
	manager, err := h.ctl(a, "show", "--property=Version")
	if err != nil {
		return err
	}
	versions, err := parseProperties(manager, "Version")
	if err != nil || !validText(versions["Version"], 511) {
		return errors.New("running user-manager version unavailable")
	}
	a.EntryRuntime.Host.Manager = versions["Version"]
	b, err := h.call(s.Path, []string{"--system", "--no-pager", "show", "--all", "--property=LoadState,ActiveState,Job", "--", "systemd-oomd.service"})
	if err != nil {
		return err
	}
	p, err := parseProperties(b, "LoadState,ActiveState,Job")
	if err != nil || !noJob(p) || p["ActiveState"] != "inactive" || p["LoadState"] != "loaded" && p["LoadState"] != "not-found" {
		return errors.New("unsupported: oomd policy unknown or active")
	}
	return nil
}

func (h hostIO) entryTerminalExit(a *Attempt) error {
	b, err := h.ctl(a, "show", "--all", "--property="+entryServiceProperties, "--", a.Service)
	if err != nil {
		return err
	}
	p, err := parseProperties(b, entryServiceProperties)
	if err != nil {
		return err
	}
	// Retain the failed transient service until the controller consumes this
	// precise natural outcome. Garbage collection cannot substitute an exit code.
	if p["InvocationID"] != a.Workload.Properties["InvocationID"] || p["Id"] != a.Service || p["Slice"] != a.Slice || p["Description"] != "ataniq-qualification-"+a.ID || p["ActiveState"] != "failed" || p["Result"] != "exit-code" || p["ExecMainCode"] != "1" || p["ExecMainStatus"] != "1" || !noJob(p) || p["MainPID"] != "0" || p["Restart"] != "no" || !commandMatches(a, p["ExecStart"]) {
		return errors.New("expected natural probe exit 1 unproved")
	}
	return nil
}

func (c *Campaign) stopEntryParent(a *Attempt, h hostIO) error {
	if err := c.Save(a, "empty", ""); err != nil {
		return err
	}
	if _, err := h.ctl(a, "stop", "--", a.Slice); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	b, err := h.ctl(a, "show", "--all", "--property="+parentProperties, "--", a.Slice)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	p, err := parseProperties(b, parentProperties)
	if err != nil || p["Id"] != a.Slice || p["ActiveState"] != "inactive" || !noJob(p) || p["ControlGroup"] != "" || p["LoadState"] != "loaded" && p["LoadState"] != "not-found" || p["LoadState"] == "loaded" && p["InvocationID"] != a.Parent.Properties["InvocationID"] {
		return c.unknown(a, "cleanup-unknown", errors.New("stopped entry parent unproved"))
	}
	a.SliceStopped = true
	return c.Save(a, "complete", a.Reason)
}

// Cleanup only. Reopening never reconstructs lost live observations, publishes
// either release, launches a unit, or promotes incomplete entry evidence.
func (c *Campaign) reconcileEntry(a *Attempt, h hostIO) error {
	// A restart gets a fixed cleanup budget of its own; it receives no launch
	// or publication authority from an expired attempt horizon.
	h = entryOperationIO(h, entryNow(h).Add(15*time.Second))
	c.checkEntryIntegrity(a)
	if a.Phase == "complete" {
		return c.Save(a, "complete", a.Reason)
	}
	if a.EntryRuntime != nil {
		retainEntryIntents(a)
		a.EntryRuntime.Natural = false
		a.EntryRuntime.ExpectedExit = false
	}
	if a.Empty && a.Parent != nil {
		stopped, err := h.entryStoppedAfterEmpty(a)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		if stopped {
			a.SliceStopped = true
			return c.Save(a, "complete", a.Reason)
		}
	}
	prelaunch := a.Workload == nil && a.EntryRuntime != nil && a.EntryRuntime.ParentStartIntent && !a.EntryRuntime.ToolDispatchIntent
	a.Stage = "entry-reconcile-parent"
	parent, err := h.parent(a)
	if err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if a.Parent == nil {
		// A successful public StartTransientUnit with lost acknowledgement is
		// adoptable only from the fresh intent and exact fixed parent properties.
		if a.EntryRuntime == nil || !a.EntryRuntime.ParentStartIntent {
			return c.unknown(a, "cleanup-unknown", errors.New("original entry parent missing"))
		}
		a.Parent = parent
		if err := c.Save(a, "slice-bound", ""); err != nil {
			return err
		}
	}
	pop, err := h.population(parent.Boundary)
	populationErr := err
	if err != nil && prelaunch {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if a.Workload == nil {
		if prelaunch && populationErr == nil && !pop && a.EntryRuntime != nil && !a.EntryRuntime.WrapperIntent {
			b, err := h.ctl(a, "show", "--all", "--property="+freshProperties, "--", a.Service)
			if err != nil {
				return c.unknown(a, "cleanup-unknown", err)
			}
			p, err := parseProperties(b, freshProperties)
			if err == nil && p["Id"] == a.Service && p["LoadState"] == "not-found" && p["ActiveState"] == "inactive" && p["ControlGroup"] == "" && p["FragmentPath"] == "" && noJob(p) {
				// No wrapper was released; exact absence plus the same empty
				// parent permits prelaunch cleanup, never an actor observation.
				a.EntryRuntime.ParentOnly, a.Empty = true, true
				return c.stopEntryParent(a, h)
			}
		}
		u, err := h.service(a)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		a.Workload = u
		// Service-generation authority is independent of the injector receipt.
		// Save this partial adoption before any stop, even if that file is lost.
		if err := c.Save(a, "cleanup-unknown", a.Reason); err != nil {
			return err
		}
	}
	var injectionErr error
	if a.Injector == nil && a.Target == nil {
		i, err := readInjection(filepath.Join(c.Path, a.ID, "injection.json"))
		if err == nil {
			err = validateInjection(a, i, a.Workload)
		}
		if err != nil {
			injectionErr = errors.New("original entry injection evidence unavailable")
		} else {
			a.Injector, a.Target = &i, &i.Process
			if err := c.Save(a, "cleanup-unknown", a.Reason); err != nil {
				return err
			}
		}
	}
	if pop || populationErr != nil {
		u, err := h.service(a)
		if err != nil || !sameUnit(u, a.Workload) {
			return c.unknown(a, "cleanup-unknown", errors.New("entry stop generation unproved"))
		}
		a.EntryRuntime.ServiceStopIntent = true
		// Unknown population withholds emptiness/completion, but does not
		// invalidate this independently checked current service generation.
		reason := a.Reason
		if populationErr != nil {
			reason = hostReason(populationErr)
		}
		if err := c.Save(a, "cancelling", reason); err != nil {
			return err
		}
		if _, err := h.ctl(a, "stop", "--", a.Service); err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
	}
	deadline := entryNow(h).Add(3 * time.Second)
	for n := 0; n < 300; n++ {
		pop, err = h.population(parent.Boundary)
		if err != nil {
			return c.unknown(a, "cleanup-unknown", err)
		}
		if !pop {
			break
		}
		if !entryNow(h).Before(deadline) {
			break
		}
		h.pause()
	}
	if pop {
		return c.unknown(a, "cleanup-unknown", errors.New("entry parent remains populated"))
	}
	if err := h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if a.EntryRuntime.ServiceStopIntent {
		a.EntryRuntime.ServiceStopped = true
	}
	if a.Injector == nil || a.Target == nil {
		return c.unknown(a, "cleanup-unknown", errors.Join(injectionErr, errors.New("original entry termination identities absent; service terminal only")))
	}
	originals := []Process{a.Injector.Process, *a.Target}
	if r := a.EntryRuntime; r != nil {
		originals = append(originals, r.Members...)
	}
	if h.ended == nil {
		return c.unknown(a, "cleanup-unknown", errors.New("entry original termination observer unavailable"))
	}
	for _, p := range originals {
		dead, err := h.ended(p)
		if err != nil || !dead {
			return c.unknown(a, "cleanup-unknown", errors.New("original entry member termination unproved"))
		}
	}
	if err := h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	if _, err := h.parent(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	pop, err = h.population(parent.Boundary)
	if err != nil || pop {
		return c.unknown(a, "cleanup-unknown", errors.New("retained entry parent empty unproved"))
	}
	if err := h.terminal(a); err != nil {
		return c.unknown(a, "cleanup-unknown", err)
	}
	a.Empty, a.TargetExited = true, true
	if a.EntryRuntime != nil {
		a.EntryRuntime.Terminal = true
	}
	return c.stopEntryParent(a, h)
}

// Saved emptiness precedes parent stop. If its acknowledgement is lost, a
// same-boot terminal/absent unit with no job may finish cleanup without trying
// to reconstruct a vanished cgroup or any missing live entry observation.
func (h hostIO) entryStoppedAfterEmpty(a *Attempt) (bool, error) {
	self, err := h.process(os.Getpid())
	if err != nil || self.Boot != a.Boot {
		return false, errors.New("entry cleanup boot changed")
	}
	b, err := h.ctl(a, "show", "--all", "--property="+parentProperties, "--", a.Slice)
	if err != nil {
		return false, err
	}
	p, err := parseProperties(b, parentProperties)
	if err != nil {
		return false, err
	}
	if p["ActiveState"] == "active" {
		return false, nil
	}
	if p["Id"] != a.Slice || p["ActiveState"] != "inactive" || !noJob(p) || p["ControlGroup"] != "" || p["LoadState"] != "loaded" && p["LoadState"] != "not-found" || p["LoadState"] == "loaded" && p["InvocationID"] != a.Parent.Properties["InvocationID"] {
		return false, errors.New("stopped original entry parent unknown")
	}
	if !entryParentOnly(a) {
		if a.Injector == nil || a.Target == nil || h.ended == nil {
			return false, errors.New("original entry cleanup identities absent")
		}
		originals := []Process{a.Injector.Process, *a.Target}
		if a.EntryRuntime != nil {
			originals = append(originals, a.EntryRuntime.Members...)
		}
		for _, original := range originals {
			dead, err := h.ended(original)
			if err != nil || !dead {
				return false, errors.New("original entry termination unknown")
			}
		}
		if err := h.terminal(a); err != nil {
			return false, err
		}
	}
	return true, nil
}
