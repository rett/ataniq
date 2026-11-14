//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

const entryPolicyProperties = "Id,LoadState,ActiveState,Job,InvocationID,ControlGroup,ManagedOOMSwap,ManagedOOMMemoryPressure,OOMRules"
const entryControllerProperties = "MainPID,ExecStart,Slice,Transient,Type,Restart,RuntimeMaxUSec,TimeoutStopUSec,MemoryMax,MemorySwapMax,MemoryHigh,TasksMax,CPUQuotaPerSecUSec,OOMPolicy,Delegate,KillMode,SendSIGKILL,FinalKillSignal"
const entryDaemonProperties = "LoadState,ActiveState,Job,InvocationID,ControlGroup,StateChangeTimestampMonotonic"

// Runtime evidence only: never participates in the plan/request/context hash.
// Identities occur once. Two bounded snapshots retain the configured-policy
// observations, not a claim about all maps held asynchronously by oomd.
type entryAdmission struct {
	Version    int             `json:"version"`
	User       entryManager    `json:"user"`
	System     entryManager    `json:"system"`
	Controller entryController `json:"controller"`
	Snapshots  []entrySnapshot `json:"snapshots"`
}

type entryIdentity struct {
	PID    int    `json:"pid"`
	UID    uint32 `json:"uid"`
	Boot   string `json:"boot"`
	Start  uint64 `json:"start"`
	Cgroup string `json:"cgroup"`
}

type entryManager struct {
	Bus      string        `json:"bus"`
	Owner    string        `json:"owner"`
	Version  string        `json:"version"`
	Identity entryIdentity `json:"identity"`
}

type entryController struct {
	Identity   entryIdentity `json:"identity"`
	Executable FileIdentity  `json:"executable"`
	Command    string        `json:"command"` // SHA256 of the exact NUL-delimited argv.
	Invocation string        `json:"invocation"`
	Settings   []string      `json:"settings"` // Fixed entryControllerProperties order, excluding ExecStart.
}

type entryPolicy struct {
	System       bool   `json:"system,omitempty"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	FileIdentity        // Flat to stay within the existing eight-level record bound.
	Invocation   string `json:"invocation"`
	Swap         string `json:"swap"`
	Pressure     string `json:"pressure"`
	Rules        string `json:"rules"`
	Max          string `json:"max,omitempty"`
	Current      uint64 `json:"current,omitempty"`
	Group        string `json:"group,omitempty"`
	OOMPolicy    string `json:"oom_policy,omitempty"`
}

type entrySnapshot struct {
	CapturedNS  int64         `json:"captured_ns"`
	AvailableKB uint64        `json:"available_kb"`
	Daemon      []string      `json:"daemon"` // Exact fixed property order above.
	Units       []entryPolicy `json:"units"`
	Result      string        `json:"result"`
}

const entryPolicyResult = "no applicable configured oomd kill policy observed"

func entryManagerEnvironment() []string {
	runtime := "/run/user/" + strconv.Itoa(os.Getuid())
	return []string{"LANG=C", "LC_ALL=C", "HOME=" + os.Getenv("HOME"), "XDG_RUNTIME_DIR=" + runtime, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtime + "/bus"}
}

// Only readable kernel metadata is required for manager identity. In particular
// PID 1's exe and namespace links need not be accessible to the qualifying user.
func entryKernelIdentity(pid int) (entryIdentity, error) {
	p := entryIdentity{PID: pid}
	if pid < 1 {
		return p, errors.New("invalid entry identity pid")
	}
	var err error
	p.Boot, err = bootID()
	if err != nil {
		return p, err
	}
	base := fmt.Sprintf("/proc/%d", pid)
	stat, err := kernelRead(base+"/stat", 8192)
	if err != nil {
		return p, err
	}
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 {
		return p, errors.New("invalid entry kernel stat")
	}
	f := strings.Fields(string(stat)[end+2:])
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return p, errors.New("entry identity not live")
	}
	p.Start, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil || p.Start == 0 {
		return p, errors.New("invalid entry start")
	}
	b, err := kernelRead(base+"/cgroup", 4096)
	if err != nil {
		return p, err
	}
	line := strings.TrimSuffix(string(b), "\n")
	if !strings.HasPrefix(line, "0::/") || strings.Contains(line, "\n") {
		return p, errors.New("entry unified cgroup unavailable")
	}
	p.Cgroup = strings.TrimPrefix(line, "0::")
	b, err = kernelRead(base+"/status", 16384)
	if err != nil {
		return p, err
	}
	seen := false
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		f := strings.Fields(line)
		if seen || len(f) != 5 || f[1] != f[2] || f[1] != f[3] || f[1] != f[4] {
			return p, errors.New("entry uid ambiguous")
		}
		n, err := strconv.ParseUint(f[1], 10, 32)
		if err != nil {
			return p, err
		}
		p.UID, seen = uint32(n), true
	}
	after, err := kernelRead(base+"/stat", 8192)
	ended, check := originalEnded(p.Start, string(after))
	if err != nil || check != nil || ended || !seen || !absolute(p.Cgroup) {
		return p, errors.New("entry kernel identity changed")
	}
	return p, nil
}

func (h hostIO) entryIdentity(pid int) (entryIdentity, error) {
	if h.entryMetadata != nil {
		return h.entryMetadata(pid)
	}
	return entryKernelIdentity(pid)
}

func (h hostIO) entryFileRead(path string, cap int64) ([]byte, error) {
	if h.entryFile != nil {
		return h.entryFile(path, cap)
	}
	return kernelRead(path, cap)
}

func (h hostIO) entryShow(a *Attempt, system bool, name, required string) (map[string]string, error) {
	s, _, err := subject(a.Config.Systemctl, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["systemctl"] {
		return nil, errors.New("entry systemctl changed")
	}
	scope := "--user"
	if system {
		scope = "--system"
	}
	args := []string{scope, "--no-pager", "show", "--all", "--property=" + required}
	if name != "" {
		args = append(args, "--", name)
	}
	b, err := h.call(s.Path, args)
	if err != nil {
		return nil, err
	}
	if len(b) > 8192 {
		return nil, errors.New("entry property observation excessive")
	}
	return parseProperties(b, required)
}

func (h hostIO) entryBus(a *Attempt, system bool, method string, args ...string) (string, error) {
	s, _, err := subject(a.Config.Busctl, true, false, false, 64<<20)
	if err != nil || s != a.Subjects["busctl"] {
		return "", errors.New("entry busctl changed")
	}
	connection := "--address=unix:path=/run/user/" + strconv.Itoa(os.Getuid()) + "/bus"
	if system {
		connection = "--system"
	}
	b, err := h.call(s.Path, append([]string{connection, "--timeout=5s", "call", "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", method}, args...))
	f := strings.Fields(b)
	if err != nil || len(f) != 2 || len(b) > 256 {
		return "", errors.New("entry public bus credentials unavailable")
	}
	if method == "GetId" || method == "GetNameOwner" {
		if f[0] != "s" || len(f[1]) < 3 || f[1][0] != '"' || f[1][len(f[1])-1] != '"' {
			return "", errors.New("invalid entry bus string")
		}
		return f[1][1 : len(f[1])-1], nil
	}
	if f[0] != "u" {
		return "", errors.New("invalid entry bus credential")
	}
	return f[1], nil
}

func (h hostIO) entryManager(a *Attempt, system bool) (entryManager, error) {
	m := entryManager{}
	var err error
	m.Bus, err = h.entryBus(a, system, "GetId")
	if err != nil || !bounded(m.Bus, 32, 32, isHex) {
		return m, errors.New("entry bus identity unknown")
	}
	m.Owner, err = h.entryBus(a, system, "GetNameOwner", "s", "org.freedesktop.systemd1")
	if err != nil || !strings.HasPrefix(m.Owner, ":1.") {
		return m, errors.New("entry manager owner unknown")
	}
	if _, err := strconv.ParseUint(strings.TrimPrefix(m.Owner, ":1."), 10, 64); err != nil {
		return m, err
	}
	pid, err := h.entryBus(a, system, "GetConnectionUnixProcessID", "s", m.Owner)
	if err != nil {
		return m, err
	}
	uid, err := h.entryBus(a, system, "GetConnectionUnixUser", "s", m.Owner)
	if err != nil {
		return m, err
	}
	n, err := strconv.Atoi(pid)
	if err != nil || n < 1 {
		return m, errors.New("entry manager pid invalid")
	}
	m.Identity, err = h.entryIdentity(n)
	wantUID := uint32(os.Getuid())
	if system {
		wantUID = 0
	}
	if err != nil || m.Identity.PID != n || uid != strconv.FormatUint(uint64(wantUID), 10) || m.Identity.UID != wantUID || m.Identity.Boot != a.Boot || system && n != 1 {
		return m, errors.New("entry manager credentials/kernel mismatch")
	}
	p, err := h.entryShow(a, system, "", "Version")
	if err != nil || !validText(p["Version"], 511) {
		return m, errors.New("entry running manager version unknown")
	}
	m.Version = p["Version"]
	return m, nil
}

func entryAncestry(path string) ([]string, error) {
	if !absolute(path) {
		return nil, errors.New("noncanonical entry ancestry")
	}
	var paths []string
	for {
		if len(paths) == 32 {
			return nil, errors.New("entry ancestry depth exceeded")
		}
		paths = append(paths, path)
		if path == "/" {
			return paths, nil
		}
		path = filepath.Dir(path)
	}
}

func entryLocations(a *Attempt, controller, root string, final bool) ([]entryPolicy, error) {
	paths, err := entryAncestry(controller)
	if err != nil {
		return nil, err
	}
	var units []entryPolicy
	for _, path := range paths {
		name := filepath.Base(path)
		if path == "/" {
			name = "-.slice"
		}
		sys := path == root || !strings.HasPrefix(path, root+"/")
		units = append(units, entryPolicy{System: sys, Name: name, Path: path})
		if path == root {
			units = append(units, entryPolicy{Name: "-.slice", Path: path})
		}
	}
	if final {
		units = append(units, entryPolicy{Name: a.Slice, Path: root + "/" + a.Slice}, entryPolicy{Name: a.Service, Path: root + "/" + a.Slice + "/" + a.Service})
	}
	seen := map[string]bool{}
	for _, unit := range units {
		key := strconv.FormatBool(unit.System) + ":" + unit.Name
		if seen[key] {
			return nil, errors.New("ambiguous entry unit mapping")
		}
		seen[key] = true
	}
	return units, nil
}

func (h hostIO) entryPolicyUnit(a *Attempt, system bool, path, name string) (entryPolicy, error) {
	u := entryPolicy{System: system, Name: name}
	p, err := h.entryShow(a, system, name, entryPolicyProperties)
	if err != nil {
		return u, err
	}
	if p["Id"] != name || p["LoadState"] != "loaded" || p["ActiveState"] != "active" || !noJob(p) || p["ControlGroup"] != path {
		return u, errors.New("entry policy unit mapping unavailable")
	}
	if p["ManagedOOMSwap"] != "auto" || p["ManagedOOMMemoryPressure"] != "auto" || p["OOMRules"] != "" {
		return u, errors.New("unsupported: applicable configured oomd policy unsafe or unknown")
	}
	u.Invocation, u.Swap, u.Pressure, u.Rules = p["InvocationID"], p["ManagedOOMSwap"], p["ManagedOOMMemoryPressure"], p["OOMRules"]
	if u.Invocation != "" && !bounded(u.Invocation, 32, 32, isHex) {
		return u, errors.New("entry unit generation malformed")
	}
	if strings.HasSuffix(name, ".service") && !bounded(u.Invocation, 32, 32, isHex) {
		return u, errors.New("entry service generation absent")
	}
	var bound Boundary
	if path == "/" {
		// boundary() deliberately excludes root for cleanup authority. The
		// observer alone may bind the root directory, without memory.max.
		if h.realHost {
			f, err := os.Open("/sys/fs/cgroup")
			if err != nil {
				return u, err
			}
			fi, err := f.Stat()
			f.Close()
			if err != nil {
				return u, err
			}
			bound = Boundary{path, identity(fi)}
		} else {
			bound, err = h.boundary(path)
		}
	} else {
		bound, err = h.boundary(path)
	}
	if err != nil || bound.Path != path || bound.Identity.Inode == 0 {
		return u, errors.New("entry policy boundary unknown")
	}
	u.Path, u.FileIdentity = bound.Path, bound.Identity
	return u, nil
}

func (u entryPolicy) boundary() Boundary { return Boundary{u.Path, u.FileIdentity} }

func (h hostIO) entryController(a *Attempt, root string) (entryController, error) {
	p, err := h.process(os.Getpid())
	if err != nil {
		return entryController{}, err
	}
	id, err := h.entryIdentity(os.Getpid())
	c := entryController{Identity: id, Executable: p.Executable}
	if err != nil || id.PID != p.PID || id.Start != p.Start || id.Boot != a.Boot || id.Boot != p.Boot || id.Cgroup != p.Cgroup || id.UID != uint32(os.Getuid()) || p.Executable != a.Subjects["driver"].Identity {
		return c, errors.New("entry qualifier identity unverified")
	}
	if filepath.Dir(id.Cgroup) != root || !strings.HasSuffix(id.Cgroup, ".service") || filepath.Base(id.Cgroup) == a.Service {
		return c, errors.New("unsupported: exact direct-root qualifier controller required")
	}
	cmd, err := h.entryFileRead(fmt.Sprintf("/proc/%d/cmdline", p.PID), 2048)
	if err != nil {
		return c, err
	}
	argv := strings.Split(strings.TrimSuffix(string(cmd), "\x00"), "\x00")
	if len(argv) != 5 || argv[0] != a.Config.Driver || argv[1] != "run" || argv[2] != "--allow-host-operations" || argv[3] != "--config" || !absolute(argv[4]) {
		return c, errors.New("entry controller must host the normal qualifier run command")
	}
	for _, arg := range argv {
		if strings.ContainsAny(arg, " \t\n\r\\\"'{};") {
			return c, errors.New("unsupported controller command encoding")
		}
	}
	c.Command = syntheticDigest(cmd)
	props, err := h.entryShow(a, false, filepath.Base(id.Cgroup), entryPolicyProperties+","+entryControllerProperties)
	if err != nil {
		return c, err
	}
	if props["LoadState"] != "loaded" || props["ActiveState"] != "active" || props["ManagedOOMSwap"] != "auto" || props["ManagedOOMMemoryPressure"] != "auto" || props["OOMRules"] != "" {
		return c, errors.New("entry controller policy/state unsafe or changing")
	}
	want := "path=" + argv[0] + " ; argv[]=" + strings.Join(argv, " ") + " ;"
	if strings.Count(props["ExecStart"], "{") != 1 || !strings.Contains(props["ExecStart"], want) || props["ControlGroup"] != id.Cgroup || props["Id"] != filepath.Base(id.Cgroup) || !bounded(props["InvocationID"], 32, 32, isHex) || !noJob(props) {
		return c, errors.New("entry controller generation/command unverified")
	}
	c.Invocation = props["InvocationID"]
	for _, key := range strings.Split(entryControllerProperties, ",") {
		if key != "ExecStart" {
			c.Settings = append(c.Settings, props[key])
		}
	}
	if !reflect.DeepEqual(c.Settings, entryControllerSettings(p.PID)) {
		return c, errors.New("entry controller fixed settings unverified")
	}
	read := h.entryRead
	if read == nil {
		read = entryKernel
	}
	for key, want := range map[string]string{"memory.max": "536870912", "memory.swap.max": "0", "memory.high": "max", "memory.oom.group": "0", "pids.max": "128", "cpu.max": "100000 100000"} {
		b, err := read(id.Cgroup, key)
		if err != nil || strings.TrimSpace(string(b)) != want {
			return c, fmt.Errorf("entry controller effective %s unverified", key)
		}
	}
	return c, nil
}

func entryControllerSettings(pid int) []string {
	return []string{strconv.Itoa(pid), "-.slice", "yes", "exec", "no", "1min 30s", "5s", "536870912", "0", "infinity", "128", "1s", "continue", "no", "control-group", "yes", "9"}
}

// Direct metadata only after wrapper publication: manager commands must stay
// outside the actor observation window. Recovery never calls this admission gate.
func (h hostIO) entryControllerAlive(a *Attempt) error {
	if a.EntryRuntime == nil || a.EntryRuntime.Admission == nil {
		return errors.New("entry controller binding absent")
	}
	want := a.EntryRuntime.Admission.Controller.Identity
	got, err := h.entryIdentity(want.PID)
	if err != nil || got != want {
		return errors.New("entry controller identity lost")
	}
	return nil
}

func (h hostIO) entryHeadroom(u *entryPolicy, shared bool) error {
	if u.Path == "/" {
		return nil
	}
	read := h.entryRead
	if read == nil {
		read = entryKernel
	}
	values := make([]string, 3)
	for j, key := range []string{"memory.max", "memory.current", "memory.oom.group"} {
		b, err := read(u.Path, key)
		if err != nil {
			return err
		}
		values[j] = strings.TrimSpace(string(b))
	}
	u.Max, u.Group = values[0], values[2]
	var err error
	u.Current, err = strconv.ParseUint(values[1], 10, 64)
	if err != nil || u.Group != "0" && u.Group != "1" {
		return errors.New("entry ancestor kernel state invalid")
	}
	if !entryMemorySafe(u.Max, u.Current, shared) {
		return errors.New("entry ancestor headroom unsafe")
	}
	after, err := h.boundary(u.Path)
	if err != nil || after != u.boundary() {
		return errors.New("entry ancestor boundary changed")
	}
	return nil
}

func entryMemorySafe(max string, current uint64, shared bool) bool {
	if max == "max" {
		return true
	}
	limit, err := strconv.ParseUint(max, 10, 64)
	return err == nil && current <= limit && (!shared || limit-current >= 384<<20)
}

func entryAvailable(b []byte) (uint64, error) {
	var available uint64
	seen := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "MemAvailable:" {
			continue
		}
		if seen || len(f) != 3 || f[2] != "kB" {
			return 0, errors.New("entry global headroom ambiguous")
		}
		seen = true
		var err error
		available, err = strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return 0, err
		}
	}
	if !seen || available < 512<<10 {
		return 0, errors.New("entry global headroom insufficient")
	}
	return available, nil
}

func entryDaemonValid(d []string) bool {
	if len(d) != 6 {
		return false
	}
	if (d[0] != "loaded" && d[0] != "not-found") || (d[1] != "active" && d[1] != "inactive") || (d[2] != "" && d[2] != "0") {
		return false
	}
	if d[3] != "" && !bounded(d[3], 32, 32, isHex) || d[4] != "" && !absolute(d[4]) {
		return false
	}
	if d[1] == "active" && (d[0] != "loaded" || d[3] == "" || d[4] == "") {
		return false
	}
	if d[0] == "not-found" && (d[1] != "inactive" || d[3] != "" || d[4] != "") {
		return false
	}
	n, err := strconv.ParseUint(d[5], 10, 64)
	return err == nil && (d[1] != "active" || n > 0)
}

func (h hostIO) observeEntryAdmission(a *Attempt, final bool) error {
	user, err := h.entryManager(a, false)
	if err != nil {
		return err
	}
	system, err := h.entryManager(a, true)
	if err != nil {
		return err
	}
	root := filepath.Dir(user.Identity.Cgroup)
	if filepath.Base(user.Identity.Cgroup) != "init.scope" || filepath.Base(root) != "user@"+strconv.Itoa(os.Getuid())+".service" || system.Identity.Cgroup != "/init.scope" {
		return errors.New("entry manager cgroup ownership unknown")
	}
	controller, err := h.entryController(a, root)
	if err != nil {
		return err
	}
	next := &entryAdmission{Version: 1, User: user, System: system, Controller: controller}
	old := a.EntryRuntime.Admission
	if final && (old == nil || len(old.Snapshots) != 1 || old.User != user || old.System != system || !reflect.DeepEqual(old.Controller, controller)) || !final && old != nil {
		return errors.New("entry manager/controller identity changed")
	}
	snap := entrySnapshot{CapturedNS: entryNow(h).UnixNano(), Result: entryPolicyResult}
	// A component-by-component walk admits no unexplained intermediate cgroup.
	locations, err := entryLocations(a, controller.Identity.Cgroup, root, false)
	if err != nil {
		return err
	}
	for _, location := range locations {
		path := location.Path
		u, err := h.entryPolicyUnit(a, location.System, path, location.Name)
		if err != nil {
			return err
		}
		if err := h.entryHeadroom(&u, true); err != nil {
			return err
		}
		if path == controller.Identity.Cgroup && u.Invocation != controller.Invocation {
			return errors.New("entry controller policy generation changed")
		}
		if path == root && location.System {
			p, err := h.entryShow(a, true, location.Name, "OOMPolicy")
			if err != nil || p["OOMPolicy"] != "continue" || u.Group != "0" {
				return errors.New("entry shared user-manager OOM handling unsafe")
			}
			u.OOMPolicy = p["OOMPolicy"]
		}
		snap.Units = append(snap.Units, u)
	}
	if final {
		for _, unit := range []*Unit{a.Parent, a.Workload} {
			if unit == nil {
				return errors.New("entry target binding absent")
			}
			want := root + "/" + a.Slice
			if unit == a.Workload {
				want += "/" + a.Service
			}
			if unit.Boundary.Path != want {
				return errors.New("entry target ancestry mismatch")
			}
			var current *Unit
			var err error
			if unit == a.Parent {
				current, err = h.parent(a)
			} else {
				current, err = h.service(a)
			}
			if err != nil || !sameUnit(unit, current) {
				return errors.New("entry effective target changed")
			}
			u, err := h.entryPolicyUnit(a, false, want, unit.Properties["Id"])
			if err != nil {
				return err
			}
			if u.boundary() != unit.Boundary || u.Invocation != unit.Properties["InvocationID"] {
				return errors.New("entry target generation changed")
			}
			if err := h.entryEffective(unit, unit == a.Parent); err != nil {
				return err
			}
			if err := h.entryHeadroom(&u, false); err != nil {
				return err
			}
			if unit == a.Workload {
				u.OOMPolicy = current.Properties["OOMPolicy"]
			}
			snap.Units = append(snap.Units, u)
		}
	}
	p, err := h.entryShow(a, true, "systemd-oomd.service", entryDaemonProperties)
	if err != nil {
		return err
	}
	for _, key := range strings.Split(entryDaemonProperties, ",") {
		snap.Daemon = append(snap.Daemon, p[key])
	}
	if !entryDaemonValid(snap.Daemon) {
		return errors.New("entry oomd state changing, malformed or unknown")
	}
	b, err := h.entryFileRead("/proc/meminfo", 8192)
	if err != nil {
		return err
	}
	snap.AvailableKB, err = entryAvailable(b)
	if err != nil {
		return err
	}
	// Repeat public credentials/kernel metadata around the entire sample.
	u2, err := h.entryManager(a, false)
	if err != nil || u2 != user {
		return errors.New("entry user manager changed during observation")
	}
	s2, err := h.entryManager(a, true)
	if err != nil || s2 != system {
		return errors.New("entry system manager changed during observation")
	}
	c2, err := h.entryController(a, root)
	if err != nil || !reflect.DeepEqual(c2, controller) {
		return errors.New("entry controller changed during observation")
	}
	d2, err := h.entryShow(a, true, "systemd-oomd.service", entryDaemonProperties)
	if err != nil || !reflect.DeepEqual(d2, p) {
		return errors.New("entry oomd changed during observation")
	}
	if final {
		if !reflect.DeepEqual(old.Snapshots[0].Daemon, snap.Daemon) {
			return errors.New("entry oomd generation changed")
		}
		for j, before := range old.Snapshots[0].Units {
			if j >= len(snap.Units) {
				return errors.New("entry ancestry disappeared")
			}
			after := snap.Units[j]
			before.Current, after.Current = 0, 0
			if before != after {
				return errors.New("entry ancestor policy/generation changed")
			}
		}
		next.Snapshots = append(next.Snapshots, old.Snapshots[0])
	}
	next.Snapshots = append(next.Snapshots, snap)
	a.EntryRuntime.Admission = next
	return nil
}

// This predicate gates new publication and success only. Stored-record/cleanup
// validators intentionally do not require the optional new runtime evidence.
func entryAdmitted(a *Attempt) bool {
	if a.EntryRuntime == nil || a.EntryRuntime.Admission == nil {
		return false
	}
	e := a.EntryRuntime.Admission
	root := filepath.Dir(e.User.Identity.Cgroup)
	if e.Version != 1 || len(e.Snapshots) != 2 {
		return false
	}
	if filepath.Base(e.User.Identity.Cgroup) != "init.scope" || filepath.Base(root) != "user@"+strconv.Itoa(os.Getuid())+".service" || e.System.Identity.PID != 1 || e.System.Identity.Cgroup != "/init.scope" {
		return false
	}
	if filepath.Dir(e.Controller.Identity.Cgroup) != root || !strings.HasSuffix(e.Controller.Identity.Cgroup, ".service") || e.Controller.Executable != a.Subjects["driver"].Identity || !validDigest(e.Controller.Command) || !bounded(e.Controller.Invocation, 32, 32, isHex) || !reflect.DeepEqual(e.Controller.Settings, entryControllerSettings(e.Controller.Identity.PID)) {
		return false
	}
	for j, m := range []entryManager{e.User, e.System} {
		uid := uint32(os.Getuid())
		if j == 1 {
			uid = 0
		}
		if m.Identity.UID != uid || m.Identity.PID < 1 || m.Identity.Start == 0 || m.Identity.Boot != a.Boot || !validBoot(m.Identity.Boot) || !bounded(m.Bus, 32, 32, isHex) || !strings.HasPrefix(m.Owner, ":1.") || !validText(m.Version, 511) {
			return false
		}
	}
	if e.Controller.Identity.PID < 1 || e.Controller.Identity.Start == 0 || e.Controller.Identity.UID != uint32(os.Getuid()) || e.Controller.Identity.Boot != a.Boot {
		return false
	}
	for j, s := range e.Snapshots {
		want, err := entryLocations(a, e.Controller.Identity.Cgroup, root, j == 1)
		if err != nil || s.Result != entryPolicyResult || s.CapturedNS <= 0 || j > 0 && s.CapturedNS < e.Snapshots[0].CapturedNS || s.AvailableKB < 512<<10 || len(s.Daemon) != 6 || len(s.Units) != len(want) {
			return false
		}
		d := s.Daemon
		if !entryDaemonValid(d) {
			return false
		}
		if j == 1 && !reflect.DeepEqual(d, e.Snapshots[0].Daemon) {
			return false
		}
		for k, u := range s.Units {
			location := want[k]
			if u.System != location.System || u.Name != location.Name || u.Path != location.Path || u.Inode == 0 || u.Swap != "auto" || u.Pressure != "auto" || u.Rules != "" {
				return false
			}
			if (u.Invocation != "" || strings.HasSuffix(u.Name, ".service")) && !bounded(u.Invocation, 32, 32, isHex) {
				return false
			}
			if u.Path != "/" {
				if u.Group != "0" && u.Group != "1" {
					return false
				}
				if !entryMemorySafe(u.Max, u.Current, k < len(e.Snapshots[0].Units)) {
					return false
				}
			}
			if u.Path == root && u.Group != "0" {
				return false
			}
			if u.Path == root && u.System && u.OOMPolicy != "continue" {
				return false
			}
			if k == 0 && (u.Invocation != e.Controller.Invocation || u.Max != "536870912" || u.Group != "0") {
				return false
			}
			if j == 1 && k < len(e.Snapshots[0].Units) {
				before, after := e.Snapshots[0].Units[k], u
				before.Current, after.Current = 0, 0
				if before != after {
					return false
				}
			}
		}
	}
	last := e.Snapshots[1].Units
	if a.Parent == nil || a.Workload == nil || len(last) < 2 {
		return false
	}
	for j, unit := range []*Unit{a.Parent, a.Workload} {
		u := last[len(last)-2+j]
		max := "268435456"
		if j == 1 {
			max = "100663296"
		}
		if u.boundary() != unit.Boundary || u.Invocation != unit.Properties["InvocationID"] || u.Name != unit.Properties["Id"] || u.System || u.Max != max || j == 1 && (u.Group != "1" || u.OOMPolicy != "kill") {
			return false
		}
	}
	return true
}
