package noaicheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fixture is a temporary tree standing for a host's root folder.
type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, root: t.TempDir()}
}

func (f *fixture) abs(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func (f *fixture) file(rel, content string) {
	f.t.Helper()
	f.dir(filepath.Dir(rel))
	if err := os.WriteFile(f.abs(rel), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) dir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.abs(rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) link(rel, target string) {
	f.t.Helper()
	f.dir(filepath.Dir(rel))
	if err := os.Symlink(target, f.abs(rel)); err != nil {
		f.t.Fatal(err)
	}
}

// unreadable makes the folder rel unreadable for the rest of the test.
func (f *fixture) unreadable(rel string) {
	f.t.Helper()
	f.dir(rel)
	if err := os.Chmod(f.abs(rel), 0); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { os.Chmod(f.abs(rel), 0o755) })
}

// testLists are small lists for the tests.
func testLists() *Lists {
	return &Lists{
		AINames:           []string{"claude", "codex", "cursor-agent-longname"},
		AIHomePaths:       []string{".claude", ".config/opencode"},
		ForbiddenPackages: []string{"nodejs", "npm"},
		PackagesAllow:     nil,
		UnitsAllow:        []string{"sshd.service", "getty@.service", "systemd-timesyncd.service"},
		PortsAllow:        []int{22, 443},
	}
}

// cleanFixture is a small host with nothing to report: a merged /usr with
// /bin, /sbin and /usr/sbin as links, users in etc/passwd (one with "/" as its
// home, one whose home is missing, one outside /home), environment files
// without provider keys, an enabled allowed unit with an absolute link, a
// process, an allowed package and a listening allowed port.
func cleanFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.file("etc/passwd", "root:x:0:0::/root:/bin/bash\n"+
		"bin:x:1:1::/:/usr/bin/nologin\n"+
		"nobody:x:65534:65534:Nobody:/nonexistent:/usr/bin/nologin\n"+
		"user:x:1000:1000::/home/user:/bin/bash\n"+
		"app:x:900:900::/srv/app:/usr/bin/nologin\n"+
		"broken line\n")
	f.file("usr/bin/bash", "#!binary\n")
	f.file("usr/bin/ls", "#!binary\n")
	f.file("usr/bin/claude-helper", "#!binary\n")
	f.link("bin", "usr/bin")
	f.link("sbin", "usr/bin")
	f.link("usr/sbin", "bin")
	f.dir("usr/local/bin")
	f.dir("opt/vendor/app/bin")
	f.file("usr/share/doc/claude", "a name outside a binary folder\n")
	f.dir("root")
	f.file("home/user/.bashrc", "export PATH=$HOME/.local/bin:$PATH\nalias ll='ls -l'\n")
	f.file("home/user/.local/bin/tool", "#!binary\n")
	f.file("home/user/.config/app/settings", "x\n")
	f.dir("srv/app")
	f.file("etc/environment", "# no keys\nLANG=C.UTF-8\n")
	f.file("etc/profile.d/locale.sh", "export LANG=C.UTF-8\n[ \"$TERM\" = dumb ] && unset PS1\n")
	f.file("usr/lib/systemd/system/sshd.service", "[Service]\nExecStart=/usr/bin/sshd -D\nEnvironment=LANG=C\n")
	f.link("etc/systemd/system/multi-user.target.wants/sshd.service", "/usr/lib/systemd/system/sshd.service")
	f.link("etc/systemd/system/getty.target.wants/getty@tty1.service", "/usr/lib/systemd/system/getty@.service")
	f.link("etc/systemd/system/display-manager.service", "/dev/null")
	f.file("proc/1/comm", "systemd\n")
	f.link("proc/1/exe", "/usr/lib/systemd/systemd")
	f.file("proc/42/comm", "bash\n")
	f.link("proc/42/exe", "/usr/bin/bash (deleted)")
	f.link("proc/self", "42")
	f.file("var/lib/pacman/local/ALPM_DB_VERSION", "9\n")
	f.file("var/lib/pacman/local/bash-5.2.037-1/desc", "%NAME%\nbash\n\n%VERSION%\n5.2.037-1\n")
	f.file("proc/net/tcp", tcpHeader+
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0\n"+
		"   1: 0100007F:9C40 0100007F:0016 01 00000000:00000000 00:00000000 00000000  1000        0 2 1 0 20 4 30 10 -1\n")
	f.file("proc/net/tcp6", tcpHeader+
		"   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0 100 0 0 10 0\n")
	return f
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// checker opens a Checker on the fixture.
func (f *fixture) checker(l *Lists) *Checker {
	f.t.Helper()
	c, err := Open(f.root, l)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c
}

// within runs fn and fails the test if it takes longer than a few seconds,
// the stand-in for a hang.
func within(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the check did not finish")
	}
}

type checkCase struct {
	name  string
	plant func(f *fixture)
	want  []Hit
}

// runCases plants each case on a clean fixture, runs one check and compares
// the hits.
func runCases(t *testing.T, cases []checkCase, check func(c *Checker)) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			if tc.plant != nil {
				tc.plant(f)
			}
			c := f.checker(testLists())
			within(t, func() { check(c) })
			got := c.Report().Hits
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hits\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder whatever its mode")
	}
}
