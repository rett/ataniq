package assert

import (
	"fmt"
	"strings"
	"testing"
)

// A table of invented mounts in the form of proc_pid_mountinfo(5): one with
// optional fields, one with escapes, a bind with a root.
const sampleMountInfo = `22 1 0:21 / / rw,relatime shared:1 - tmpfs tmpfs rw,size=1024k
23 22 0:5 / /dev rw,nosuid,relatime - devtmpfs devtmpfs rw,size=4k,nr_inodes=1024
31 22 259:2 /srv/state/leads/a/home /home/lead rw,nosuid,nodev,relatime master:2 shared:3 - ext4 /dev/example rw
32 22 259:2 /srv/owned/managed/a/settings.json /etc/claude-code/managed-settings.json ro,nosuid,nodev,relatime - ext4 /dev/example rw
33 31 259:2 /srv/state/leads/a/odd\040name\011tab\134x /home/lead/odd\040dir ro,relatime - ext4 /dev/example\040two ro,errors=remount-ro
`

func TestParseMountInfo(t *testing.T) {
	ms, err := ParseMountInfo([]byte(sampleMountInfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 5 {
		t.Fatalf("%d mounts", len(ms))
	}
	if m := ms[2]; m.ID != 31 || m.ParentID != 22 || m.Root != "/srv/state/leads/a/home" || m.Point != "/home/lead" || m.FSType != "ext4" || m.Source != "/dev/example" || m.ReadOnly() {
		t.Errorf("%+v", m)
	}
	if m := ms[3]; !m.ReadOnly() || m.Point != "/etc/claude-code/managed-settings.json" {
		t.Errorf("%+v", m)
	}
	m := ms[4]
	if m.Root != "/srv/state/leads/a/odd name\ttab\\x" || m.Point != "/home/lead/odd dir" || m.Source != "/dev/example two" || !m.ReadOnly() {
		t.Errorf("escapes: %+v", m)
	}
	// ro in the file system's options alone counts, "rw" does not, and a
	// name that merely contains "ro" does not.
	for _, tc := range []struct {
		opts, super string
		ro          bool
	}{
		{"rw", "rw", false}, {"ro", "rw", true}, {"rw", "ro", true},
		{"rw,errors=remount-ro", "rw", false}, {"rw,nosuid", "rw,noro", false},
	} {
		if got := (Mount{Options: tc.opts, SuperOptions: tc.super}).ReadOnly(); got != tc.ro {
			t.Errorf("ReadOnly(%s, %s) = %v", tc.opts, tc.super, got)
		}
	}
}

func TestParseMountInfoRefusals(t *testing.T) {
	for name, in := range map[string]string{
		"too few fields":   "22 1 0:21 / / rw\n",
		"no separator":     "22 1 0:21 / / rw shared:1 tmpfs tmpfs rw\n",
		"short after sep":  "22 1 0:21 / / rw - tmpfs tmpfs\n",
		"bad id":           "x 1 0:21 / / rw - tmpfs tmpfs rw\n",
		"bad parent":       "22 y 0:21 / / rw - tmpfs tmpfs rw\n",
		"bad escape":       "22 1 0:21 / /a\\zzz rw - tmpfs tmpfs rw\n",
		"truncated escape": "22 1 0:21 / /a\\04 rw - tmpfs tmpfs rw\n",
	} {
		_, err := ParseMountInfo([]byte(in))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if r := refusalOf(t, err); r.Field != "mount table" || !strings.Contains(r.Reason, "line 1") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The bound on the number of mounts.
	var b strings.Builder
	for i := 0; i < MaxMounts+1; i++ {
		fmt.Fprintf(&b, "%d 1 0:1 / /m%d rw - tmpfs tmpfs rw\n", i, i)
	}
	if _, err := ParseMountInfo([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many mounts: %v", err)
	}
	if ms, err := ParseMountInfo(nil); err != nil || len(ms) != 0 {
		t.Errorf("empty table: %v %v", ms, err)
	}
}
