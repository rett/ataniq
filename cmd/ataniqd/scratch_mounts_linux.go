//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// This file places a folder within its filesystem, for the rules of
// --owner-settings (scratch_settings_linux.go). A path names a folder of one
// mount; /proc/self/mountinfo says, for each mount, which filesystem it shows
// (its device), which folder of that filesystem it shows (its root) and where
// (its mount point). The place of a path is its device with the mount's root
// joined to the part of the path below the mount point: two paths that name
// one folder, through a bind mount or any other mount of the same filesystem,
// have one place, and a folder lies within another exactly when its place does.
// The names of a place are the paths at which the mounts of the table show it,
// or a part of it. A mount root other than "/" is ordinary (a btrfs subvolume
// shows /@home, a bind mount the folder it binds) and is never refused by
// itself.
//
// What stays outside: the table is the daemon's own mount namespace, read once
// at the check; a mount made afterwards, or in another namespace, is not seen.
// Only the owner or root can make a mount on the host; a lead cannot.

// mountinfoPath is the mount table of the daemon's own mount namespace.
const mountinfoPath = "/proc/self/mountinfo"

// maxMountinfoBytes bounds the table read.
const maxMountinfoBytes = 16 << 20

// mountEntry is one line of /proc/self/mountinfo: the mount's id and its
// parent's, the filesystem's device (major:minor), the folder of the
// filesystem it shows (root) and where it shows it (point).
type mountEntry struct {
	id, parent  int
	dev         string
	root, point string
}

// mountTable is the mount table, in the order of the file.
type mountTable []mountEntry

// fsPlace is where a path lies within its filesystem: the device and the path
// from the filesystem's own root.
type fsPlace struct{ dev, path string }

// unescapeMount undoes the escapes of a path of mountinfo: a space, a tab, a
// newline and a backslash are written as three octal digits after a
// backslash. Any other backslash is refused.
func unescapeMount(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+4 > len(s) {
			return "", fmt.Errorf("a cut escape in %q", s)
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", fmt.Errorf("an escape that is not three octal digits in %q", s)
		}
		b.WriteByte(byte(v))
		i += 3
	}
	return b.String(), nil
}

// parseMountinfo reads the text of /proc/self/mountinfo. Each line is the
// mount id, the parent's id, major:minor, the root, the mount point, then
// options this check does not use. A line it cannot read is an error: a table
// read in part would place a folder wrongly.
func parseMountinfo(text string) (mountTable, error) {
	var out mountTable
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			return nil, fmt.Errorf("line %d of the mount table has %d fields", n, len(f))
		}
		id, err1 := strconv.Atoi(f[0])
		parent, err2 := strconv.Atoi(f[1])
		maj, min, ok := strings.Cut(f[2], ":")
		_, err3 := strconv.ParseUint(maj, 10, 32)
		_, err4 := strconv.ParseUint(min, 10, 32)
		if err1 != nil || err2 != nil || !ok || err3 != nil || err4 != nil {
			return nil, fmt.Errorf("line %d of the mount table does not start with two ids and major:minor", n)
		}
		root, err := unescapeMount(f[3])
		if err != nil {
			return nil, fmt.Errorf("line %d of the mount table: %v", n, err)
		}
		point, err := unescapeMount(f[4])
		if err != nil {
			return nil, fmt.Errorf("line %d of the mount table: %v", n, err)
		}
		if !filepath.IsAbs(point) {
			return nil, fmt.Errorf("line %d of the mount table: the mount point %q is not absolute", n, point)
		}
		out = append(out, mountEntry{id: id, parent: parent, dev: f[2], root: root, point: filepath.Clean(point)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("the mount table is empty")
	}
	return out, nil
}

// byID is the mount of the table with the id.
func (t mountTable) byID(id int) (mountEntry, bool) {
	for _, m := range t {
		if m.id == id {
			return m, true
		}
	}
	return mountEntry{}, false
}

// below is the part of p below q ("" when p is q, else "/a/b"), for p under q.
func below(p, q string) string {
	if q == "/" {
		if p == "/" {
			return ""
		}
		return p
	}
	return p[len(q):]
}

// placeOf is the place of the absolute clean path p, which lies on the mount
// id: the mount's device, and its root joined to the part of p below its
// mount point.
func (t mountTable) placeOf(id int, p string) (fsPlace, error) {
	m, ok := t.byID(id)
	if !ok {
		return fsPlace{}, fmt.Errorf("its mount (%d) is not in the mount table", id)
	}
	if !under(p, m.point) {
		return fsPlace{}, fmt.Errorf("it lies on the mount %d at %s, which does not hold it", id, m.point)
	}
	return fsPlace{dev: m.dev, path: filepath.Clean(m.root + "/" + below(p, m.point))}, nil
}

// namesOf are the paths at which a mount of the table shows the place, or a
// part of it: for a mount of the same filesystem whose root holds the place,
// its mount point joined to the part of the place below the root; for one
// whose root lies within the place, its mount point.
func (t mountTable) namesOf(pl fsPlace) []string {
	var out []string
	for _, m := range t {
		if m.dev != pl.dev {
			continue
		}
		switch {
		case under(pl.path, m.root):
			out = append(out, filepath.Clean(m.point+"/"+below(pl.path, m.root)))
		case under(m.root, pl.path):
			out = append(out, m.point)
		}
	}
	return out
}

// placeMeet is how a folder and a guard meet by their places: not at all, the
// folder is or lies in the guard, or the folder holds it. how says by which
// rule, for the refusal.
type placeMeet struct {
	in, holds bool
	how       string
}

// meet decides a folder (its name a, its place pa) against a guard (name b,
// place pb) on the table: the folder is or lies in the guard when its place is
// or lies in the guard's, on one device, or when a mount shows it, or a part of
// it, at a name that is or lies in a name of the guard; it holds the guard in
// the same ways the other way round. The names a and b are compared by path
// by the caller first.
func (t mountTable) meet(a string, pa fsPlace, b string, pb fsPlace) placeMeet {
	if pa.dev == pb.dev {
		if under(pa.path, pb.path) {
			return placeMeet{in: true, how: fmt.Sprintf("in its filesystem (device %s) it is %s, which is or lies in %s (a bind mount gives a second name)", pa.dev, pa.path, pb.path)}
		}
		if under(pb.path, pa.path) {
			return placeMeet{holds: true, how: fmt.Sprintf("in its filesystem (device %s) it is %s, which holds %s (a bind mount gives a second name)", pa.dev, pa.path, pb.path)}
		}
	}
	an, bn := t.namesOf(pa), t.namesOf(pb)
	if !hasPath(an, a) {
		an = append(an, a)
	}
	if !hasPath(bn, b) {
		bn = append(bn, b)
	}
	for _, x := range an {
		for _, y := range bn {
			if x == a && y == b {
				continue
			}
			if under(x, y) {
				return placeMeet{in: true, how: fmt.Sprintf("a mount shows the folder, or a part of it, at %s, within %s", x, y)}
			}
			if under(y, x) {
				return placeMeet{holds: true, how: fmt.Sprintf("a mount shows the guard, or a part of it, at %s, within %s", y, x)}
			}
		}
	}
	return placeMeet{}
}

func hasPath(list []string, p string) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// readMountTable reads and parses the daemon's own mount table.
func readMountTable() (mountTable, error) {
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMountinfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxMountinfoBytes {
		return nil, fmt.Errorf("the mount table is larger than %d bytes", maxMountinfoBytes)
	}
	return parseMountinfo(string(b))
}

// mountIDOfFD is the id of the mount the open file lies on, as the mount
// table numbers it: statx's STATX_MNT_ID, or the mnt_id line of the file's
// fdinfo on a kernel without it.
func mountIDOfFD(fd int) (int, error) {
	var stx unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stx)
	if err == nil && stx.Mask&unix.STATX_MNT_ID != 0 {
		return int(stx.Mnt_id), nil
	}
	b, rerr := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if rerr != nil {
		if err == nil {
			err = rerr
		}
		return 0, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "mnt_id:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, errors.New("the mount of the file is not known (no STATX_MNT_ID and no mnt_id in fdinfo)")
}

// mountIDOfPath is the id of the mount the resolved path rp lies on: the mount
// of its nearest part that is there (a path that is not there yet lies on the
// mount of the folder it would be made in). No link is followed.
func mountIDOfPath(rp string) (int, error) {
	cur := rp
	for {
		fd, err := unix.Open(cur, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil {
			defer unix.Close(fd)
			return mountIDOfFD(fd)
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
			return 0, err
		}
		if cur == "/" {
			return 0, errors.New("no part of the path exists")
		}
		cur = filepath.Dir(cur)
	}
}
