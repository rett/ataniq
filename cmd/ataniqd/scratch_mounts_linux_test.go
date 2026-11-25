//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"slices"
	"strings"
	"testing"
)

// Tests of the places of scratch_mounts_linux.go on mount tables written out
// here, as /proc/self/mountinfo writes them: no mount is made.

// A plain host: an ext4 root, /proc and a tmpfs /tmp.
const mountsPlain = `22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw
23 22 0:21 / /proc rw,nosuid,nodev,noexec shared:12 - proc proc rw
24 22 0:30 / /tmp rw,nosuid,nodev shared:5 - tmpfs tmpfs rw,size=8g
`

// mountsWith is the plain host with more lines.
func mountsWith(lines ...string) string {
	return mountsPlain + strings.Join(lines, "\n") + "\n"
}

// A btrfs root with the subvolumes /@ at /, /@home at /home and /@log at
// /var/log, and the top of the filesystem at /mnt/top.
const mountsBtrfs = `30 1 0:35 /@ / rw,relatime shared:1 - btrfs /dev/nvme0n1p2 rw,subvolid=256,subvol=/@
31 30 0:35 /@home /home rw,relatime shared:2 - btrfs /dev/nvme0n1p2 rw,subvolid=257,subvol=/@home
32 30 0:35 /@log /var/log rw,relatime shared:3 - btrfs /dev/nvme0n1p2 rw,subvolid=258,subvol=/@log
33 30 0:35 / /mnt/top rw,relatime shared:4 - btrfs /dev/nvme0n1p2 rw,subvolid=5,subvol=/
34 30 0:22 / /proc rw - proc proc rw
`

func mustTable(t *testing.T, text string) mountTable {
	t.Helper()
	tbl, err := parseMountinfo(text)
	if err != nil {
		t.Fatalf("parseMountinfo: %v", err)
	}
	return tbl
}

func mustPlace(t *testing.T, tbl mountTable, id int, p string) fsPlace {
	t.Helper()
	pl, err := tbl.placeOf(id, p)
	if err != nil {
		t.Fatalf("placeOf(%d, %s): %v", id, p, err)
	}
	return pl
}

// The fields a line gives, and the lines that are refused.
func TestParseMountinfo(t *testing.T) {
	tbl := mustTable(t, mountsWith(`50 22 8:2 /data/my\040files /mnt/my\040view rw - ext4 /dev/sda2 rw`, `51 22 8:2 /a\134b /mnt/tab\011x rw - ext4 /dev/sda2 rw`))
	if len(tbl) != 5 {
		t.Fatalf("%d mounts: %+v", len(tbl), tbl)
	}
	if got := tbl[0]; got != (mountEntry{id: 22, parent: 1, dev: "8:2", root: "/", point: "/"}) {
		t.Errorf("the root mount: %+v", got)
	}
	if got := tbl[3]; got.root != "/data/my files" || got.point != "/mnt/my view" {
		t.Errorf("escaped spaces: %+v", got)
	}
	if got := tbl[4]; got.root != `/a\b` || got.point != "/mnt/tab\tx" {
		t.Errorf("an escaped backslash and tab: %+v", got)
	}
	for _, bad := range []string{
		"",
		"\n\n",
		"22 1 8:2 / \n",
		"x 1 8:2 / / rw - ext4 /dev/sda2 rw\n",
		"22 1 82 / / rw - ext4 /dev/sda2 rw\n",
		"22 1 8:2 / relative rw - ext4 /dev/sda2 rw\n",
		`22 1 8:2 /a\04 / rw - ext4 /dev/sda2 rw` + "\n",
		`22 1 8:2 /a\09x / rw - ext4 /dev/sda2 rw` + "\n",
		mountsPlain + "garbage\n",
	} {
		if _, err := parseMountinfo(bad); err == nil {
			t.Errorf("parseMountinfo took %q", bad)
		}
	}
}

// A place is the mount's device and its root joined to the path below its
// mount point; a path the mount does not hold, or a mount the table does not
// list, is refused.
func TestMountPlaceOf(t *testing.T) {
	plain := mustTable(t, mountsWith(`50 22 8:2 /data/my\040files /mnt/my\040view rw - ext4 /dev/sda2 rw`))
	for _, c := range []struct {
		tbl  mountTable
		id   int
		p    string
		want fsPlace
	}{
		{plain, 22, "/", fsPlace{"8:2", "/"}},
		{plain, 22, "/srv/proof/owner-settings", fsPlace{"8:2", "/srv/proof/owner-settings"}},
		{plain, 24, "/tmp", fsPlace{"0:30", "/"}},
		{plain, 24, "/tmp/x/y", fsPlace{"0:30", "/x/y"}},
		{plain, 50, "/mnt/my view/proof", fsPlace{"8:2", "/data/my files/proof"}},
		{mustTable(t, mountsBtrfs), 31, "/home/u/proof/owner-settings", fsPlace{"0:35", "/@home/u/proof/owner-settings"}},
		{mustTable(t, mountsBtrfs), 30, "/usr", fsPlace{"0:35", "/@/usr"}},
		{mustTable(t, mountsBtrfs), 33, "/mnt/top/@home/u", fsPlace{"0:35", "/@home/u"}},
	} {
		if got := mustPlace(t, c.tbl, c.id, c.p); got != c.want {
			t.Errorf("placeOf(%d, %s) = %+v, want %+v", c.id, c.p, got, c.want)
		}
	}
	if _, err := plain.placeOf(99, "/x"); err == nil {
		t.Error("a mount the table does not list was placed")
	}
	if _, err := plain.placeOf(24, "/tmpx/y"); err == nil {
		t.Error("a path the mount does not hold was placed")
	}
}

// meetOf decides the folder a on mount ia against the guard b on mount ib.
func meetOf(t *testing.T, tbl mountTable, a string, ia int, b string, ib int) placeMeet {
	t.Helper()
	return tbl.meet(a, mustPlace(t, tbl, ia, a), b, mustPlace(t, tbl, ib, b))
}

// The folder and a guard on each table: apart on a plain host and on btrfs
// subvolumes; in a guard through a bind of a folder within it, given as the
// folder; in a lead's folder through a bind of the folder, or a part of it,
// into it; holding a guard through a bind of the guard into it.
func TestMountMeet(t *testing.T) {
	const (
		settings = "/srv/proof/owner-settings"
		owner    = "/srv/state/owner"
		lead     = "/srv/state/leads/scratch-standin-1-lead"
	)
	type want int
	const (
		apart want = iota
		in
		holds
	)
	for _, c := range []struct {
		name   string
		text   string
		a      string
		ia     int
		b      string
		ib     int
		want   want
		phrase string
	}{
		{"plain: apart", mountsPlain, settings, 22, owner, 22, apart, ""},
		{"plain: apart from a guard that is not there yet", mountsPlain, settings, 22, lead + "/home", 22, apart, ""},
		{"plain: on another filesystem", mountsPlain, "/tmp/proof/owner-settings", 24, owner, 22, apart, ""},
		{"a bind of a folder within owner/, given as the folder",
			mountsWith("40 22 8:2 /srv/state/owner/below /srv/view rw - ext4 /dev/sda2 rw"),
			"/srv/view", 40, owner, 22, in, "in its filesystem (device 8:2) it is /srv/state/owner/below, which is or lies in /srv/state/owner"},
		{"a folder within that bind",
			mountsWith("40 22 8:2 /srv/state/owner/below /srv/view rw - ext4 /dev/sda2 rw"),
			"/srv/view/deeper", 40, owner, 22, in, "/srv/state/owner/below/deeper"},
		{"a bind of owner/ itself",
			mountsWith("40 22 8:2 /srv/state/owner /srv/view rw - ext4 /dev/sda2 rw"),
			"/srv/view", 40, owner, 22, in, "which is or lies in"},
		{"a bind of the state folder, which holds owner/",
			mountsWith("40 22 8:2 /srv/state /srv/view rw - ext4 /dev/sda2 rw"),
			"/srv/view", 40, owner, 22, holds, "it is /srv/state, which holds /srv/state/owner"},
		{"the settings folder bound into a lead's folder",
			mountsWith("41 22 8:2 /srv/proof/owner-settings /srv/state/leads/scratch-standin-1-lead/home/x rw - ext4 /dev/sda2 rw"),
			settings, 22, lead, 22, in, "a mount shows the folder, or a part of it, at /srv/state/leads/scratch-standin-1-lead/home/x, within " + lead},
		{"a folder within the settings folder bound into a lead's folder",
			mountsWith("41 22 8:2 /srv/proof/owner-settings/part /srv/state/leads/scratch-standin-1-lead/home/x rw - ext4 /dev/sda2 rw"),
			settings, 22, lead, 22, in, "at /srv/state/leads/scratch-standin-1-lead/home/x"},
		{"the folder above the settings folder bound into a lead's folder",
			mountsWith("41 22 8:2 /srv/proof /srv/state/leads/scratch-standin-1-lead/home/x rw - ext4 /dev/sda2 rw"),
			settings, 22, lead, 22, in, "at /srv/state/leads/scratch-standin-1-lead/home/x/owner-settings"},
		{"owner/ bound into the settings folder",
			mountsWith("42 22 8:2 /srv/state/owner /srv/proof/owner-settings/o rw - ext4 /dev/sda2 rw"),
			settings, 22, owner, 22, holds, "a mount shows the guard, or a part of it, at /srv/proof/owner-settings/o, within " + settings},
		{"a tmpfs folder bound into a lead's folder",
			mountsWith("43 22 0:30 /proof/owner-settings /srv/state/leads/scratch-standin-1-lead/s rw - tmpfs tmpfs rw"),
			"/tmp/proof/owner-settings", 24, lead, 22, in, "at /srv/state/leads/scratch-standin-1-lead/s"},
		{"btrfs: apart across subvolumes, with the top mounted too", mountsBtrfs,
			"/home/u/proof/owner-settings", 31, "/usr", 30, apart, ""},
		{"btrfs: apart within one subvolume", mountsBtrfs,
			"/home/u/proof/owner-settings", 31, "/home/u/state/owner", 31, apart, ""},
		{"btrfs: the settings folder within owner/ through the top", mountsBtrfs,
			"/mnt/top/@home/u/state/owner/s", 33, "/home/u/state/owner", 31, in, "it is /@home/u/state/owner/s, which is or lies in /@home/u/state/owner"},
		{"btrfs: a bind of a folder within owner/",
			mountsBtrfs + "35 30 0:35 /@home/u/state/owner/below /srv/view rw - btrfs /dev/nvme0n1p2 rw\n",
			"/srv/view", 35, "/home/u/state/owner", 31, in, "(device 0:35) it is /@home/u/state/owner/below"},
		{"escaped spaces: a bind of owner/ within a folder whose name has a space",
			mountsWith(`44 22 8:2 /srv/my\040state/owner/below /mnt/my\040view rw - ext4 /dev/sda2 rw`),
			"/mnt/my view", 44, "/srv/my state/owner", 22, in, "it is /srv/my state/owner/below"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tbl := mustTable(t, c.text)
			m := meetOf(t, tbl, c.a, c.ia, c.b, c.ib)
			switch {
			case c.want == apart && (m.in || m.holds):
				t.Errorf("not apart: %+v", m)
			case c.want == in && !m.in, c.want == holds && !m.holds:
				t.Errorf("got %+v, want %d (1 in, 2 holds)", m, c.want)
			case c.want != apart && !strings.Contains(m.how, c.phrase):
				t.Errorf("the reason %q does not say %q", m.how, c.phrase)
			}
		})
	}
}

// The names of a place are every path a mount shows it, or a part of it, at.
func TestMountNamesOf(t *testing.T) {
	tbl := mustTable(t, mountsBtrfs+"35 31 0:35 /@home/u/proof/owner-settings/part /srv/part rw - btrfs /dev/nvme0n1p2 rw\n")
	got := tbl.namesOf(fsPlace{"0:35", "/@home/u/proof/owner-settings"})
	slices.Sort(got)
	want := []string{"/home/u/proof/owner-settings", "/mnt/top/@home/u/proof/owner-settings", "/srv/part"}
	if !slices.Equal(got, want) {
		t.Errorf("namesOf = %q, want %q", got, want)
	}
	if got := tbl.namesOf(fsPlace{"0:99", "/x"}); len(got) != 0 {
		t.Errorf("a place no mount shows has names: %q", got)
	}
}
