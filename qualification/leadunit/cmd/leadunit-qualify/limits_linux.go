//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"ataniq/internal/leadunit"
)

// host is what the limit checks need of the machine outside the unit manager:
// running the one tool they use, systemctl, and reading the control group and
// runtime folders. The real one (realLife) runs systemctl through the manager's
// own runner and reads the files; every test supplies a fake, so that no test
// runs systemctl or reads a real control group.
type host interface {
	// Run runs systemctl with argv (argv[0] is "systemctl") and returns its
	// output.
	Run(ctx context.Context, argv []string) (string, error)
	// ReadFile reads the file at an absolute path.
	ReadFile(path string) ([]byte, error)
	// ReadDir returns the names of the entries of the folder at an absolute
	// path. An error that wraps fs.ErrNotExist means the folder is absent.
	ReadDir(path string) ([]string, error)
}

// infinite is the normalised value of a limit that is not set: systemd's
// "infinity" and the control group files' "max".
const infinite = uint64(math.MaxUint64)

// The three properties of a limit, in the order the checks read them, and the
// control group file that holds each.
var limitProps = []struct {
	prop string
	file string
}{
	{"MemoryMax", "memory.max"},
	{"MemorySwapMax", "memory.swap.max"},
	{"TasksMax", "pids.max"},
}

// limitValues is the three limits as normalised numbers (bytes, bytes, tasks),
// in the order of limitProps.
type limitValues [3]uint64

func valuesOf(l leadunit.Limits) limitValues {
	return limitValues{l.MemoryMax, l.SwapMax, l.TasksMax}
}

func (v limitValues) String() string {
	return fmt.Sprintf("MemoryMax=%s MemorySwapMax=%s TasksMax=%s", showNumber(v[0]), showNumber(v[1]), showNumber(v[2]))
}

func showNumber(n uint64) string {
	if n == infinite {
		return "infinity"
	}
	return strconv.FormatUint(n, 10)
}

// parseNumber reads one value as systemd or the control group files print it,
// into bytes or a count: a decimal number, "infinity" or "max" (infinite), a
// number with a suffix K, M, G, T, P or E (powers of 1024, as systemd reads
// sizes and task counts), and the value of the largest 64-bit number, which
// systemd also uses for infinity. A percentage (a form that TasksMax takes as
// an input) cannot be normalised without the host's own number and is an
// error, as is anything else.
func parseNumber(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return 0, errors.New("the value is empty")
	case "infinity", "max":
		return infinite, nil
	}
	if strings.HasSuffix(s, "%") {
		return 0, fmt.Errorf("the value %q is a percentage, which cannot be normalised to a count", s)
	}
	mult := uint64(1)
	digits := s
	if i := strings.IndexByte("KMGTPE", s[len(s)-1]); i >= 0 {
		mult = uint64(1) << (10 * (i + 1))
		digits = s[:len(s)-1]
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, fmt.Errorf("the value %q is not a number", s)
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("the value %q is out of range", s)
	}
	if n != 0 && mult > infinite/n {
		return 0, fmt.Errorf("the value %q is out of range", s)
	}
	return n * mult, nil
}

// parseShow reads the answer of "systemctl show" for the three limit
// properties into normalised values. Each property must appear exactly once
// and no other may.
func parseShow(out string) (limitValues, error) {
	var v limitValues
	var seen [3]bool
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		name, val, ok := strings.Cut(line, "=")
		if !ok {
			return v, fmt.Errorf("show output line %q is not Name=value", snip(line))
		}
		idx := -1
		for i, p := range limitProps {
			if p.prop == name {
				idx = i
			}
		}
		if idx < 0 {
			return v, fmt.Errorf("show output has the property %q, which was not asked for", snip(name))
		}
		if seen[idx] {
			return v, fmt.Errorf("show output repeats %s", name)
		}
		n, err := parseNumber(val)
		if err != nil {
			return v, fmt.Errorf("%s: %w", name, err)
		}
		v[idx], seen[idx] = n, true
	}
	for i, p := range limitProps {
		if !seen[i] {
			return v, fmt.Errorf("show output lacks %s", p.prop)
		}
	}
	return v, nil
}

func snip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// showLimits reads the three properties of the unit with "systemctl --user
// show".
func (q *qualifier) showLimits(ctx context.Context) (limitValues, error) {
	props := make([]string, len(limitProps))
	for i, p := range limitProps {
		props[i] = p.prop
	}
	out, err := q.host.Run(ctx, []string{"systemctl", "--user", "show", "--all", "--property=" + strings.Join(props, ","), "--", q.unit})
	if err != nil {
		return limitValues{}, fmt.Errorf("systemctl show: %w", err)
	}
	return parseShow(out)
}

// cgroupLimits reads memory.max, memory.swap.max and pids.max in the control
// group folder of the started unit, normalised.
func (q *qualifier) cgroupLimits() (limitValues, error) {
	var v limitValues
	if q.cgroupRoot == "" {
		return v, errors.New("no control group root is configured")
	}
	cg := q.started.ControlGroup
	if cg == "" || !strings.HasPrefix(cg, "/") || filepath.Clean(cg) != cg {
		return v, fmt.Errorf("the control group %q of the unit is not a clean absolute path", cg)
	}
	dir := filepath.Join(q.cgroupRoot, cg)
	for i, p := range limitProps {
		b, err := q.host.ReadFile(filepath.Join(dir, p.file))
		if err != nil {
			return v, fmt.Errorf("read %s: %w", p.file, err)
		}
		n, err := parseNumber(string(b))
		if err != nil {
			return v, fmt.Errorf("%s: %w", p.file, err)
		}
		v[i] = n
	}
	return v, nil
}

// differences lists what differs between got and want, "" when nothing does.
// The comparison is exact for both ways of reading: the command accepts only
// memory sizes that are a whole number of pages, so that the control group
// files have no rounding to allow for.
func differences(got, want limitValues) string {
	var diff []string
	for i, p := range limitProps {
		if got[i] != want[i] {
			diff = append(diff, fmt.Sprintf("%s reads %s, expected %s", p.prop, showNumber(got[i]), showNumber(want[i])))
		}
	}
	return strings.Join(diff, "; ")
}

func (q *qualifier) haveHost() error {
	if q.host == nil {
		return errors.New("the harness has no access to systemctl and the control group files")
	}
	return nil
}

// limitsShowReadBack compares "systemctl --user show" with the specification.
func (q *qualifier) limitsShowReadBack(ctx context.Context) (string, error) {
	if err := q.haveHost(); err != nil {
		return "", err
	}
	got, err := q.showLimits(ctx)
	if err != nil {
		return "", err
	}
	if d := differences(got, valuesOf(*q.limits)); d != "" {
		return "", fmt.Errorf("systemctl show differs from the specification: %s", d)
	}
	return "systemctl --user show reads " + got.String() + ", as specified", nil
}

// limitsCgroupReadBack compares the control group files with the
// specification.
func (q *qualifier) limitsCgroupReadBack(context.Context) (string, error) {
	if err := q.haveHost(); err != nil {
		return "", err
	}
	got, err := q.cgroupLimits()
	if err != nil {
		return "", err
	}
	if d := differences(got, valuesOf(*q.limits)); d != "" {
		return "", fmt.Errorf("the control group files differ from the specification: %s", d)
	}
	return "memory.max, memory.swap.max and pids.max in " + q.started.ControlGroup + " read " + got.String() + ", as specified", nil
}

// loweredValues is the specification with the memory limit lowered.
func (q *qualifier) loweredValues() limitValues {
	v := valuesOf(*q.limits)
	v[0] = q.lowered
	return v
}

// memoryLowered lowers the memory limit once on the running unit, with
// "systemctl --user set-property --runtime", and notes what the two runtime
// folders hold for the unit just after it.
func (q *qualifier) memoryLowered(ctx context.Context) (string, error) {
	if err := q.haveHost(); err != nil {
		return "", err
	}
	if q.lowered == 0 || q.lowered >= q.limits.MemoryMax {
		return "", fmt.Errorf("the lowered memory limit %d is not below the specified %d", q.lowered, q.limits.MemoryMax)
	}
	arg := "MemoryMax=" + strconv.FormatUint(q.lowered, 10)
	if _, err := q.host.Run(ctx, []string{"systemctl", "--user", "set-property", "--runtime", "--", q.unit, arg}); err != nil {
		return "", fmt.Errorf("systemctl set-property --runtime: %w", err)
	}
	return "set " + arg + " with --runtime; " + q.runtimeFolderNote(), nil
}

// loweredShowReadBack reads the lowered limit back from systemctl show.
func (q *qualifier) loweredShowReadBack(ctx context.Context) (string, error) {
	var got limitValues
	var err error
	ok, perr := q.poll(ctx, emptyBound, func() (bool, error) {
		got, err = q.showLimits(ctx)
		return err == nil && differences(got, q.loweredValues()) == "", err
	})
	if !ok {
		if err != nil {
			return "", err
		}
		if perr != nil {
			return "", perr
		}
		return "", fmt.Errorf("systemctl show after the change differs: %s", differences(got, q.loweredValues()))
	}
	return "systemctl --user show reads " + got.String() + " after the change", nil
}

// loweredCgroupReadBack reads the lowered limit back from the control group
// files.
func (q *qualifier) loweredCgroupReadBack(ctx context.Context) (string, error) {
	var got limitValues
	var err error
	ok, perr := q.poll(ctx, emptyBound, func() (bool, error) {
		got, err = q.cgroupLimits()
		return err == nil && differences(got, q.loweredValues()) == "", err
	})
	if !ok {
		if err != nil {
			return "", err
		}
		if perr != nil {
			return "", perr
		}
		return "", fmt.Errorf("the control group files after the change differ: %s", differences(got, q.loweredValues()))
	}
	return "the control group files read " + got.String() + " after the change", nil
}

// The two runtime folders of the user manager that a unit leaves things in,
// below its runtime folder: the transient unit files and the settings that
// "set-property --runtime" writes.
var runtimeFolders = []string{"systemd/transient", "systemd/user.control"}

// entriesFor lists the names that belong to one of the units: the unit's own
// file or folder, its ".d" folder and anything else that starts with the unit's
// whole name.
func entriesFor(names []string, units ...string) []string {
	var out []string
	for _, n := range names {
		for _, u := range units {
			if strings.HasPrefix(n, u) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// runtimeFolderNote says what the two runtime folders hold for the unit now. It
// is information in the detail of the check that changes a setting, and shows a
// reader of the report what the folder check after the stop looks for.
func (q *qualifier) runtimeFolderNote() string {
	if q.runtimeDir == "" {
		return "no runtime folder is configured"
	}
	var parts []string
	for _, f := range runtimeFolders {
		names, err := q.host.ReadDir(filepath.Join(q.runtimeDir, f))
		if err != nil {
			parts = append(parts, f+": "+err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("%s holds %q for the unit", f, entriesFor(names, q.unit)))
	}
	return strings.Join(parts, "; ")
}

// runtimeFoldersClean checks, after the stop, that nothing for the run's units
// remains under the transient and user.control folders of the user manager's
// runtime folder. The units are given a bounded time to be removed. The
// manager's folder must exist, so that a wrong runtime folder cannot pass.
func (q *qualifier) runtimeFoldersClean(ctx context.Context) (string, error) {
	if err := q.haveHost(); err != nil {
		return "", err
	}
	if q.runtimeDir == "" {
		return "", errors.New("no runtime folder is configured")
	}
	if _, err := q.host.ReadDir(filepath.Join(q.runtimeDir, "systemd")); err != nil {
		return "", fmt.Errorf("the user manager's runtime folder: %w", err)
	}
	var left []string
	ok, err := q.poll(ctx, emptyBound, func() (bool, error) {
		left = left[:0]
		for _, f := range runtimeFolders {
			names, rerr := q.host.ReadDir(filepath.Join(q.runtimeDir, f))
			switch {
			case rerr == nil:
			case errors.Is(rerr, fs.ErrNotExist):
				continue // an absent folder holds nothing
			default:
				return false, fmt.Errorf("list %s: %w", f, rerr)
			}
			for _, n := range entriesFor(names, q.unit, q.childUnit) {
				left = append(left, f+"/"+n)
			}
		}
		return len(left) == 0, nil
	})
	if !ok {
		if len(left) > 0 {
			return "", fmt.Errorf("%d entries for the run's units remain after the stop: %q", len(left), left)
		}
		return "", err
	}
	return "nothing for " + q.unit + " or " + q.childUnit + " remains under " + strings.Join(runtimeFolders, " and ") + " of " + q.runtimeDir, nil
}
