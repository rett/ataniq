//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type R1EntryFD struct {
	FD       int          `json:"fd"`
	Identity FileIdentity `json:"identity"`
	Mode     uint32       `json:"mode"`
	Rdev     uint64       `json:"rdev"`
	Flags    uint64       `json:"flags"`
	MountID  uint64       `json:"mount_id"`
}

type R1EntryEndpoints struct {
	ChildStdio   []R1EntryFD  `json:"child_stdio"`
	ChildToken   R1EntryFD    `json:"child_token"`
	ParentToken  R1EntryFD    `json:"parent_token"`
	MainOutput   R1EntryFD    `json:"main_output"`
	Null         R1NullDevice `json:"null"`
	Control      FileIdentity `json:"control"`
	NullMount    string       `json:"null_mount"`
	DevMount     string       `json:"dev_mount"`
	ControlMount string       `json:"control_mount"`
	NullUID      uint32       `json:"null_uid"`
	OverflowUID  uint32       `json:"overflow_uid"`
}

func entryMembers(want Boundary) ([]Process, error) {
	before, err := boundary(want.Path)
	if err != nil || before != want {
		return nil, errors.New("entry membership boundary replaced")
	}
	d, err := openAbsolute("/sys/fs/cgroup"+want.Path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(MaxEntries + 1)
	if err != nil || len(entries) > MaxEntries {
		return nil, errors.New("entry cgroup entries inaccessible/excessive")
	}
	for _, e := range entries {
		if e.IsDir() {
			return nil, errors.New("unexpected entry cgroup branch")
		}
	}
	b, err := kernelRead("/sys/fs/cgroup"+want.Path+"/cgroup.procs", 1024)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) < 3 || len(ids) > 4 {
		return nil, errors.New("entry membership count")
	}
	var members []Process
	for _, text := range ids {
		pid, err := strconv.Atoi(text)
		if err != nil || strconv.Itoa(pid) != text {
			return nil, errors.New("invalid entry member PID")
		}
		p, err := process(pid)
		if err != nil {
			return nil, err
		}
		if p.Cgroup != want.Path {
			return nil, errors.New("entry member migrated")
		}
		members = append(members, p)
	}
	after, err := boundary(want.Path)
	if err != nil || after != want {
		return nil, errors.New("entry membership boundary changed")
	}
	return members, nil
}

func entryTree(parent, tool Boundary) error {
	if filepath.Dir(tool.Path) != parent.Path {
		return errors.New("entry Tool outside exact parent")
	}
	for _, want := range []Boundary{parent, tool} {
		b, err := boundary(want.Path)
		if err != nil || b != want {
			return errors.New("entry tree boundary changed")
		}
	}
	b, err := entryKernel(parent.Path, "cgroup.procs")
	if err != nil || len(strings.Fields(string(b))) != 0 {
		return errors.New("unknown parent-local entry member")
	}
	d, err := openAbsolute("/sys/fs/cgroup"+parent.Path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer d.Close()
	entries, err := d.ReadDir(MaxEntries + 1)
	if err != nil || len(entries) > MaxEntries {
		return errors.New("entry parent enumeration inaccessible/excessive")
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() != filepath.Base(tool.Path) {
				return errors.New("unknown entry parent branch")
			}
			count++
		}
	}
	if count != 1 {
		return errors.New("entry Tool branch missing")
	}
	for _, want := range []Boundary{parent, tool} {
		b, err := boundary(want.Path)
		if err != nil || b != want {
			return errors.New("entry tree boundary changed")
		}
	}
	return nil
}

func entryFDInfo(data []byte) (flags, mount uint64, err error) {
	seenFlags, seenMount := false, false
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "flags":
			if seenFlags {
				return 0, 0, errors.New("duplicate descriptor flags")
			}
			seenFlags = true
			flags, err = strconv.ParseUint(value, 8, 64)
		case "mnt_id":
			if seenMount {
				return 0, 0, errors.New("duplicate descriptor mount")
			}
			seenMount = true
			mount, err = strconv.ParseUint(value, 10, 64)
		}
		if err != nil {
			return 0, 0, err
		}
	}
	if !seenFlags || !seenMount || mount == 0 {
		return 0, 0, errors.New("missing descriptor flags/mount")
	}
	return flags, mount, nil
}

// Proc FD symlinks are intentional kernel metadata. Flags come from fdinfo,
// never F_GETFL on the observer's own O_PATH handle. No descriptor is read.
func entryFD(pid, fd int) (R1EntryFD, error) {
	base := fmt.Sprintf("/proc/%d", pid)
	var st syscall.Stat_t
	if err := syscall.Stat(fmt.Sprintf("%s/fd/%d", base, fd), &st); err != nil {
		return R1EntryFD{}, err
	}
	b, err := kernelRead(fmt.Sprintf("%s/fdinfo/%d", base, fd), 4096)
	if err != nil {
		return R1EntryFD{}, err
	}
	flags, mount, err := entryFDInfo(b)
	return R1EntryFD{fd, FileIdentity{Device: st.Dev, Inode: st.Ino, UID: st.Uid}, st.Mode, st.Rdev, flags, mount}, err
}

func entryMounts(data []byte) (null, dev, control string, err error) {
	root := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			return "", "", "", errors.New("invalid entry mountinfo")
		}
		var dst *string
		switch f[4] {
		case "/":
			dst = &root
		case "/dev/null":
			dst = &null
		case "/dev":
			dst = &dev
		case R1ControlTarget:
			dst = &control
		}
		if strings.HasPrefix(f[4], R1ControlTarget+"/") {
			return "", "", "", errors.New("unexpected control submount")
		}
		if dst != nil {
			if *dst != "" {
				return "", "", "", errors.New("duplicate entry mount")
			}
			*dst = line
		}
	}
	// --dir /dev normally resides on the final readonly root tmpfs; it is
	// not itself a mount. Record its actual containing mount in that layout.
	if dev == "" {
		dev = root
	}
	if null == "" || control == "" {
		return "", "", "", errors.New("selected entry mounts missing")
	}
	return
}

func projectedUID(host uint32, mapping string, overflow uint32) (uint32, error) {
	for _, line := range strings.Split(strings.TrimSpace(mapping), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			return 0, errors.New("invalid entry UID map")
		}
		v := [3]uint64{}
		for i := range f {
			n, err := strconv.ParseUint(f[i], 10, 32)
			if err != nil {
				return 0, err
			}
			v[i] = n
		}
		if v[2] == 0 || v[0]+v[2] > 1<<32 || v[1]+v[2] > 1<<32 {
			return 0, errors.New("invalid UID map range")
		}
		if uint64(host) >= v[1] && uint64(host)-v[1] < v[2] {
			return uint32(v[0] + uint64(host) - v[1]), nil
		}
	}
	return overflow, nil
}

func entryEndpoints(main, child Process) (R1EntryEndpoints, error) {
	var e R1EntryEndpoints
	for fd := 0; fd < 3; fd++ {
		d, err := entryFD(child.PID, fd)
		if err != nil {
			return e, err
		}
		e.ChildStdio = append(e.ChildStdio, d)
	}
	var err error
	e.ChildToken, err = entryFD(child.PID, 4)
	if err != nil {
		return e, err
	}
	e.MainOutput, err = entryFD(main.PID, 1)
	if err != nil {
		return e, err
	}
	// Find the exact parent write endpoint by pipe identity. Descriptor 3 is
	// deliberately unasserted: the handshake closed it and Go can reuse it.
	d, err := os.Open(fmt.Sprintf("/proc/%d/fd", main.PID))
	if err != nil {
		return e, err
	}
	defer d.Close()
	names, err := d.Readdirnames(1025)
	if err != nil || len(names) > 1024 {
		return e, errors.New("parent descriptor metadata inaccessible/excessive")
	}
	count := 0
	for _, text := range names {
		fd, err := strconv.Atoi(text)
		if err != nil || fd < 0 || fd >= 1024 {
			return e, errors.New("invalid parent descriptor")
		}
		var st syscall.Stat_t
		if err := syscall.Stat(fmt.Sprintf("/proc/%d/fd/%d", main.PID, fd), &st); err != nil {
			return e, err
		}
		if st.Dev != e.ChildToken.Identity.Device || st.Ino != e.ChildToken.Identity.Inode {
			continue
		}
		peer, err := entryFD(main.PID, fd)
		if err != nil {
			return e, err
		}
		if peer.Mode&syscall.S_IFMT != syscall.S_IFIFO || peer.Flags&syscall.O_ACCMODE != syscall.O_WRONLY {
			return e, errors.New("wrong token peer role")
		}
		e.ParentToken = peer
		count++
	}
	if count != 1 {
		return e, errors.New("token peer not unique")
	}
	base := fmt.Sprintf("/proc/%d/root", child.PID)
	var st syscall.Stat_t
	if err := syscall.Stat(base+"/dev/null", &st); err != nil {
		return e, err
	}
	e.Null = R1NullDevice{FileIdentity{Device: st.Dev, Inode: st.Ino, UID: st.Uid}, st.Mode, st.Gid, st.Rdev}
	if err := syscall.Stat(base+R1ControlTarget, &st); err != nil {
		return e, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || st.Mode&0777 != 0700 {
		return e, errors.New("wrong workload control directory")
	}
	e.Control = FileIdentity{Device: st.Dev, Inode: st.Ino, UID: st.Uid}
	b, err := kernelRead(fmt.Sprintf("/proc/%d/mountinfo", child.PID), 32768)
	if err != nil {
		return e, err
	}
	e.NullMount, e.DevMount, e.ControlMount, err = entryMounts(b)
	if err != nil {
		return e, err
	}
	// Capture the selected kernel's overflow projection once. Stored validation
	// and inspect use this evidence, never a later host sysctl value.
	overflow, err := kernelRead("/proc/sys/kernel/overflowuid", 64)
	if err != nil {
		return e, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(overflow)), 10, 32)
	if err != nil {
		return e, err
	}
	e.OverflowUID = uint32(n)
	e.NullUID, err = projectedUID(e.Null.Identity.UID, child.UIDMap, e.OverflowUID)
	return e, err
}

func mountOption(line, option string) bool {
	f := strings.Fields(line)
	return len(f) >= 10 && strings.Contains(","+f[5]+",", ","+option+",")
}

func validateEntryEndpoints(a *Attempt, main, child Process, e R1EntryEndpoints) error {
	if e.Null != a.EntryStatic.Null || e.Control != a.EntryStatic.Control || len(e.ChildStdio) != 3 || !readOnlyMount(e.ControlMount) || !mountOption(e.NullMount, "nosuid") || mountOption(e.NullMount, "nodev") || !readOnlyMount(e.DevMount) {
		return errors.New("entry null/control mount mismatch")
	}
	nullFields := strings.Fields(e.NullMount)
	devFields := strings.Fields(e.DevMount)
	controlFields := strings.Fields(e.ControlMount)
	if len(nullFields) < 10 || nullFields[4] != "/dev/null" || len(devFields) < 10 || devFields[4] != "/dev" && devFields[4] != "/" || len(controlFields) < 10 || controlFields[4] != R1ControlTarget {
		return errors.New("alternate entry mount")
	}
	mount, err := strconv.ParseUint(nullFields[0], 10, 64)
	if err != nil || mount == 0 {
		return errors.New("invalid null mount ID")
	}
	uid, err := projectedUID(e.Null.Identity.UID, child.UIDMap, e.OverflowUID)
	if err != nil || uid != e.NullUID {
		return errors.New("null ownership projection mismatch")
	}
	for fd, d := range e.ChildStdio {
		access := uint64(syscall.O_WRONLY)
		if fd == 0 {
			access = syscall.O_RDONLY
		}
		if d.FD != fd || d.Identity != e.Null.Identity || d.Mode != e.Null.Mode || d.Rdev != e.Null.Rdev || d.Flags&(syscall.O_ACCMODE|0x200000) != access || d.MountID != mount {
			return errors.New("child standard stream is not the exact null endpoint")
		}
	}
	token, peer := e.ChildToken, e.ParentToken
	if token.FD != 4 || peer.FD < 3 || peer.FD >= 1024 || token.Identity.Inode == 0 || token.Identity != peer.Identity || token.Mode&syscall.S_IFMT != syscall.S_IFIFO || peer.Mode&syscall.S_IFMT != syscall.S_IFIFO || token.Flags&(syscall.O_ACCMODE|0x200000) != syscall.O_RDONLY || peer.Flags&(syscall.O_ACCMODE|0x200000) != syscall.O_WRONLY {
		return errors.New("private token read/write pipe identity mismatch")
	}
	out := e.MainOutput
	if out.FD != 1 || out.Identity != a.Streams["output"] || out.Mode&syscall.S_IFMT != syscall.S_IFREG || out.Flags&(syscall.O_ACCMODE|syscall.O_APPEND|0x200000) != syscall.O_WRONLY|syscall.O_APPEND {
		return errors.New("main output writer mismatch")
	}
	if main.PID == child.PID {
		return errors.New("duplicate endpoint process")
	}
	return nil
}

// No manager calls occur in the live two-second observation window.
func (c *Campaign) observeEntry(a *Attempt, h hostIO, ready R1Evidence, receipt time.Time) ([]*os.File, error) {
	if err := h.entryControllerAlive(a); err != nil {
		return nil, err
	}
	if ready.Ready == nil || ready.R1Binding != a.EntryRuntime.Binding || ready.Ready.ControlDirectory != objectID(a.EntryStatic.Control) {
		c.failEntryIntegrity(a)
		return nil, errors.New("entry ready binding mismatch")
	}
	// This reserves a receipt-relative margin, not the actor's remaining wait:
	// emission can precede receipt. Final pidfd checks still require live actors.
	deadline := receipt.Add(R1ReleaseWait - 250*time.Millisecond)
	start := entryNow(h)
	if !start.Before(deadline) {
		return nil, errors.New("entry observation margin exhausted before membership")
	}
	if err := h.entryTree(a.Parent.Boundary, a.Workload.Boundary); err != nil {
		return nil, err
	}
	members, err := h.entryMembers(a.Workload.Boundary)
	if err != nil {
		return nil, err
	}
	var main, child Process
	for _, p := range members {
		if entryActorMatches(a, p, ready.Ready.Main) {
			main = p
		}
		if entryActorMatches(a, p, ready.Ready.Child) {
			child = p
		}
	}
	if err := validateEntryMembers(a, *ready.Ready, members, main, child); err != nil {
		return nil, err
	}
	membersDone := entryNow(h)
	// Keep identities in memory before opening handles or any fallible Save.
	r := a.EntryRuntime
	r.Ready, r.Main, r.Child, r.Members = &ready, &main, &child, members
	var handles []*os.File
	for _, p := range members {
		if !entryNow(h).Before(deadline) {
			return handles, errors.New("entry observation margin exhausted before pidfd")
		}
		fd, err := h.handle(p)
		if err != nil {
			return handles, err
		}
		handles = append(handles, fd)
	}
	handlesDone := entryNow(h)
	e, err := h.entryEndpoints(main, child)
	if err != nil {
		return handles, err
	}
	if err := validateEntryEndpoints(a, main, child, e); err != nil {
		return handles, err
	}
	endpointsDone := entryNow(h)
	if err := h.entryControllerAlive(a); err != nil {
		return handles, err
	}
	// Re-enumerate exact membership and identities around the metadata batch.
	after, err := h.entryMembers(a.Workload.Boundary)
	if err != nil {
		return handles, err
	}
	if len(after) != len(members) {
		return handles, errors.New("entry membership changed")
	}
	for _, p := range members {
		found := false
		for _, q := range after {
			if sameProcess(p, q) && reflectEqualNamespaces(p, q) {
				found = true
			}
		}
		if !found {
			return handles, errors.New("entry live identity changed")
		}
	}
	for _, fd := range handles {
		dead, err := h.exit(fd)
		if err != nil || dead {
			return handles, errors.New("entry actor/member ended during observation")
		}
	}
	if err := h.entryTree(a.Parent.Boundary, a.Workload.Boundary); err != nil {
		return handles, err
	}
	end := entryNow(h)
	if !end.Before(deadline) {
		return handles, errors.New("entry observation margin exhausted")
	}
	r.Endpoints, r.ReceiptNS, r.ObservationNS, r.MarginNS = &e, receipt.UnixNano(), end.Sub(start).Nanoseconds(), deadline.Sub(end).Nanoseconds()
	r.Timings = &R1EntryTimings{membersDone.Sub(start).Nanoseconds(), handlesDone.Sub(membersDone).Nanoseconds(), endpointsDone.Sub(handlesDone).Nanoseconds(), end.Sub(endpointsDone).Nanoseconds()}
	return handles, c.Save(a, "released", "")
}

func reflectEqualNamespaces(a, b Process) bool {
	for _, name := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
		if a.Namespaces[name] != b.Namespaces[name] {
			return false
		}
	}
	return true
}

// Keep trusted kernel file paths out of the caller's vocabulary.
func entryKernel(path, name string) ([]byte, error) {
	return kernelRead(filepath.Join("/sys/fs/cgroup", path, name), 4096)
}
