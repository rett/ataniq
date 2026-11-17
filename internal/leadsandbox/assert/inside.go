package assert

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"ataniq/internal/leadsandbox/profile"
)

// MaxManagedSettingsBytes is the most of the managed-settings file that is
// read. A larger file is a failure: Ataniq writes a small one.
const MaxManagedSettingsBytes = 1 << 20

// Files the probe reads to check assertion 5.
const (
	tiocstiSysctl = "/proc/sys/dev/tty/legacy_tiocsti"
	procStatus    = "/proc/self/status"

	maxSysctlBytes = 64
	maxStatusBytes = 64 << 10
)

// RunInside runs the inside checks, assertions 1 to 8, in order, over the
// system, and returns the report. It never panics on what a system returns and
// it never writes: it asks, with Lstat, ReadFile, Mounts, Environ, Dial,
// LookupHost and Writable. A check that cannot be made is not-checked, with
// the reason. The probe never issues TIOCSTI: assertion 5 reads the sysctl and
// the seccomp mode of the process.
//
// The spec should have passed Validate (DecodeSpec does it). If it has not,
// every result is not-checked, with the refusal as the reason.
func RunInside(spec Spec, sys System) Report {
	rep := Report{Version: ReportVersion, Digest: spec.Digest}
	if err := spec.Validate(); err != nil {
		for n := 1; n <= NumAssertions; n++ {
			if a, _ := ByNumber(n); a.HasInside() {
				rep.Results = append(rep.Results, result(n, StatusNotChecked, "the spec is refused: %v", err))
			}
		}
		return rep
	}
	c := &checker{spec: spec, sys: sys}
	rep.Results = []Result{
		c.holderHidden(), c.environment(), c.loopback(), c.provider(),
		c.tiocsti(), c.ownerPaths(), c.treeGit(), c.managed(),
	}
	return rep
}

type checker struct {
	spec Spec
	sys  System

	loaded    bool
	mounts    []Mount
	mountsErr error
}

// mountTable reads the mount table once.
func (c *checker) mountTable() ([]Mount, error) {
	if !c.loaded {
		c.mounts, c.mountsErr = c.sys.Mounts()
		c.loaded = true
	}
	return c.mounts, c.mountsErr
}

// result builds a result for assertion n with the ID the table gives it and a
// detail made safe by EscapeDetail.
func result(n int, st Status, format string, args ...any) Result {
	return Result{Number: n, ID: idOf(n), Status: st, Detail: EscapeDetail(fmt.Sprintf(format, args...))}
}

// tally collects what a check found: facts that fail the assertion, and
// questions it could not ask.
type tally struct {
	failed    []string
	unchecked []string
}

func (t *tally) fail(format string, args ...any) {
	t.failed = append(t.failed, fmt.Sprintf(format, args...))
}

func (t *tally) skip(format string, args ...any) {
	t.unchecked = append(t.unchecked, fmt.Sprintf(format, args...))
}

// maxListed is how many findings a detail lists before it counts the rest.
const maxListed = 3

func list(items []string) string {
	if len(items) <= maxListed {
		return strings.Join(items, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(items[:maxListed], "; "), len(items)-maxListed)
}

// result gives the status of assertion n: failed when anything failed (the
// detail also counts the questions not asked), else not-checked when anything
// could not be asked, else ok with the detail given.
func (t *tally) result(n int, ok string) Result {
	switch {
	case len(t.failed) > 0:
		d := list(t.failed)
		if len(t.unchecked) > 0 {
			d += fmt.Sprintf(" (%d more checks could not be made)", len(t.unchecked))
		}
		return result(n, StatusFailed, "%s", d)
	case len(t.unchecked) > 0:
		return result(n, StatusNotChecked, "%s", list(t.unchecked))
	}
	return result(n, StatusOK, "%s", ok)
}

// mustBeAbsent records whether path exists.
func (c *checker) mustBeAbsent(t *tally, what, path string) {
	_, err := c.sys.Lstat(path)
	switch {
	case err == nil:
		t.fail("%s %q exists", what, path)
	case absent(err):
	default:
		t.skip("%s %q: cannot tell whether it exists: %v", what, path, err)
	}
}

// readOnly records whether path is on a read-only mount and the kernel says it
// is not writable. Both must hold: access(W_OK) alone says yes to a process
// with the rights to override permissions, and the mount flag alone does not
// see a permission or a file system that would let a write through.
func (c *checker) readOnly(t *tally, what, path string) {
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("%s %q: no mount table: %v", what, path, err)
	} else if m, ok := mountFor(mounts, path); !ok {
		t.skip("%s %q: no mount of the table holds it", what, path)
	} else if !m.ReadOnly() {
		t.fail("%s %q is on a writable mount at %q", what, path, m.Point)
	}
	w, err := c.sys.Writable(path)
	switch {
	case err != nil:
		t.skip("%s %q: access(W_OK): %v", what, path, err)
	case w:
		t.fail("%s %q is writable by access(W_OK)", what, path)
	}
}

// 1. The attach and control sockets and the holder's state are not visible.
func (c *checker) holderHidden() Result {
	var t tally
	for _, p := range c.spec.HolderPaths {
		c.mustBeAbsent(&t, "holder path", p)
	}
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("no mount table: %v", err)
	}
	for _, m := range mounts {
		for _, sub := range c.spec.MountSubstrings {
			if strings.Contains(m.Point, sub) || strings.Contains(m.Root, sub) || strings.Contains(m.Source, sub) {
				t.fail("mount %q mentions %q", m.Point, sub)
				break
			}
		}
	}
	return t.result(1, fmt.Sprintf("none of %d holder paths exists; none of %d mounts mentions any of %d substrings",
		len(c.spec.HolderPaths), len(mounts), len(c.spec.MountSubstrings)))
}

// 2. The environment equals the allowlist.
func (c *checker) environment() Result {
	want := make([]profile.EnvVar, len(c.spec.Env))
	for i, e := range c.spec.Env {
		want[i] = profile.EnvVar{Name: e.Name, Value: e.Value}
	}
	diffs, err := profile.DiffEnvironment(want, c.sys.Environ())
	if err != nil {
		return result(2, StatusNotChecked, "cannot compare: %v", err)
	}
	if len(diffs) > 0 {
		parts := make([]string, len(diffs))
		for i, d := range diffs {
			parts[i] = d.String()
		}
		return result(2, StatusFailed, "%d differences: %s", len(diffs), list(parts))
	}
	return result(2, StatusOK, "the environment equals the allowlist of %d variables", len(want))
}

// unreachable says a failed dial is evidence that the address cannot be
// reached: a refusal, a timeout, no route or no such socket. Another error
// (out of descriptors, say) says the question was not asked.
func unreachable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	for _, e := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ENOENT, syscall.ENETUNREACH, syscall.EHOSTUNREACH,
		syscall.EADDRNOTAVAIL, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ETIMEDOUT,
		syscall.EACCES, syscall.EPERM,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// 3. One host loopback port and one host abstract socket are unreachable.
func (c *checker) loopback() Result {
	var t tally
	for _, target := range []struct{ what, network, addr string }{
		{"host loopback port", "tcp", c.spec.LoopbackTCP},
		{"host abstract socket", "unix", "@" + c.spec.AbstractSocket},
	} {
		err := c.sys.Dial(target.network, target.addr, c.spec.ConnectTimeout())
		switch {
		case err == nil:
			t.fail("%s %s is reachable: a connection was made", target.what, target.addr)
		case !unreachable(err):
			t.skip("%s %s: the dial failed in a way that does not show it is unreachable: %v", target.what, target.addr, err)
		}
	}
	return t.result(3, "the host loopback port and the host abstract socket could not be reached")
}

// 4. A provider host name resolves.
func (c *checker) provider() Result {
	addrs, err := c.sys.LookupHost(c.spec.ProviderHost, c.spec.ResolveTimeout())
	switch {
	case err != nil:
		return result(4, StatusFailed, "%q does not resolve: %v", c.spec.ProviderHost, err)
	case len(addrs) == 0:
		return result(4, StatusFailed, "%q resolved to no address", c.spec.ProviderHost)
	}
	return result(4, StatusOK, "%q resolves to %d addresses", c.spec.ProviderHost, len(addrs))
}

// sysctlState is what legacy_tiocsti says.
type sysctlState int

const (
	sysctlZero sysctlState = iota
	sysctlNonZero
	sysctlAbsent
	sysctlUnreadable
)

// readSysctl reads dev.tty.legacy_tiocsti and describes it for a detail.
func readSysctl(sys System) (sysctlState, string) {
	data, err := sys.ReadFile(tiocstiSysctl, maxSysctlBytes)
	switch {
	case err == nil:
		v := strings.TrimSpace(string(data))
		if v == "0" {
			return sysctlZero, "legacy_tiocsti is 0"
		}
		return sysctlNonZero, fmt.Sprintf("legacy_tiocsti is %q, not 0", v)
	case absent(err):
		return sysctlAbsent, "legacy_tiocsti is absent (the kernel does not restrict TIOCSTI, or /proc/sys is hidden)"
	}
	return sysctlUnreadable, fmt.Sprintf("legacy_tiocsti cannot be read: %v", err)
}

// seccompMode reads the Seccomp: line of /proc/self/status. A status without
// the line (a kernel without seccomp) is mode 0.
func seccompMode(sys System) (int, error) {
	data, err := sys.ReadFile(procStatus, maxStatusBytes)
	if err != nil {
		return 0, err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		v, ok := bytes.CutPrefix(line, []byte("Seccomp:"))
		if !ok {
			continue
		}
		mode, err := strconv.Atoi(string(bytes.TrimSpace(v)))
		if err != nil {
			return 0, fmt.Errorf("the Seccomp: line is not a number")
		}
		return mode, nil
	}
	return 0, nil
}

// 5. legacy_tiocsti is 0, or a seccomp filter is loaded; when the profile
// expects a filter, it must be loaded.
func (c *checker) tiocsti() Result {
	mode, merr := seccompMode(c.sys)
	if c.spec.SeccompExpected {
		switch {
		case merr != nil:
			return result(5, StatusNotChecked, "the profile expects a seccomp filter and %s cannot be read: %v", procStatus, merr)
		case mode != 2:
			return result(5, StatusFailed, "the profile expects a seccomp filter and Seccomp: is %d, not 2", mode)
		}
		return result(5, StatusOK, "a seccomp filter is loaded (Seccomp: 2), as the profile expects")
	}
	st, sdetail := readSysctl(c.sys)
	switch {
	case st == sysctlZero:
		return result(5, StatusOK, "%s", sdetail)
	case merr == nil && mode == 2:
		return result(5, StatusOK, "%s; a seccomp filter is loaded (Seccomp: 2)", sdetail)
	case st == sysctlUnreadable:
		return result(5, StatusNotChecked, "%s", sdetail)
	case merr != nil:
		return result(5, StatusNotChecked, "%s, and %s cannot be read: %v", sdetail, procStatus, merr)
	}
	return result(5, StatusFailed, "%s, and no seccomp filter is loaded (Seccomp: %d)", sdetail, mode)
}

// 6. The owner's folders and the other runtime's config folder do not exist.
func (c *checker) ownerPaths() Result {
	var t tally
	for _, p := range c.spec.OwnerPaths {
		c.mustBeAbsent(&t, "owner path", p)
	}
	note := ""
	c.otherConfig(&t, &note)
	return t.result(6, fmt.Sprintf("none of %d owner paths exists; %s", len(c.spec.OwnerPaths), note))
}

// otherConfig checks the other runtime's config folder. A lead can make an
// empty folder at that path in its own home, so the folder existing is not a
// failure of the boundary: a mount at it, or below it, is.
func (c *checker) otherConfig(t *tally, note *string) {
	dir := c.spec.OtherConfigDir
	info, err := c.sys.Lstat(dir)
	switch {
	case absent(err):
		*note = fmt.Sprintf("the other runtime's config folder %q does not exist", dir)
		return
	case err != nil:
		t.skip("the other runtime's config folder %q: cannot tell whether it exists: %v", dir, err)
		return
	}
	mounts, err := c.mountTable()
	if err != nil {
		t.skip("the other runtime's config folder %q exists and there is no mount table: %v", dir, err)
		return
	}
	for _, m := range mounts {
		if within(m.Point, dir) {
			t.fail("something is mounted at or below the other runtime's config folder: %q", m.Point)
			return
		}
	}
	*note = fmt.Sprintf("the other runtime's config folder %q exists (%s) and nothing is mounted at or below it; "+
		"a folder the lead made itself is not a failure of the boundary", dir, info.Describe())
}

// 7. The tree is the lead's own clone and its three git paths are read-only.
func (c *checker) treeGit() Result {
	var t tally
	git := c.spec.Tree + "/.git"
	info, err := c.sys.Lstat(git)
	switch {
	case absent(err):
		t.fail("%q does not exist: the tree is not a clone", git)
	case err != nil:
		t.skip("%q: %v", git, err)
	case !info.IsDir():
		t.fail("%q is %s, not a folder: the tree is not the lead's own clone", git, info.Describe())
	}
	wantDir := []bool{false, true, true} // .git/config, .git/hooks, .git/info
	for i, p := range c.spec.GitReadOnly {
		info, err := c.sys.Lstat(p)
		switch {
		case absent(err):
			t.fail("%q does not exist, so nothing read-only covers it", p)
			continue
		case err != nil:
			t.skip("%q: %v", p, err)
			continue
		case info.IsDir() != wantDir[i] || info.IsSymlink() || !info.IsDir() && !info.IsRegular():
			t.fail("%q is %s", p, info.Describe())
			continue
		}
		c.readOnly(&t, "git path", p)
	}
	return t.result(7, fmt.Sprintf("%q is a folder and its config, hooks and info are read-only", git))
}

// 8. The managed-settings file is present and read-only, and holds the marker.
func (c *checker) managed() Result {
	var t tally
	p := c.spec.ManagedSettings
	info, err := c.sys.Lstat(p)
	switch {
	case absent(err):
		return result(8, StatusFailed, "%q does not exist", p)
	case err != nil:
		return result(8, StatusNotChecked, "%q: %v", p, err)
	case !info.IsRegular():
		return result(8, StatusFailed, "%q is %s, not a regular file", p, info.Describe())
	}
	c.readOnly(&t, "managed settings", p)
	data, err := c.sys.ReadFile(p, MaxManagedSettingsBytes)
	switch {
	case errors.Is(err, ErrTooLarge):
		t.fail("%q is larger than %d bytes", p, MaxManagedSettingsBytes)
	case errors.Is(err, ErrNotRegular):
		t.fail("%q is not a regular file", p)
	case err != nil:
		t.skip("%q cannot be read: %v", p, err)
	case c.spec.ManagedMarker != "" && !bytes.Contains(data, []byte(c.spec.ManagedMarker)):
		t.fail("%q does not contain the marker %q", p, c.spec.ManagedMarker)
	}
	if c.spec.ManagedMarker == "" {
		return t.result(8, fmt.Sprintf("%q is a regular file and read-only; no marker is required for %s", p, c.spec.Runtime))
	}
	return t.result(8, fmt.Sprintf("%q is a regular file, read-only, and contains the marker", p))
}
