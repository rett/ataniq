package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

// fakeNFT puts a folder whose nft prints an empty ruleset first in the
// switch's search path, so that no test lists the firewall of the host.
func fakeNFT(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "nft"), "#!/bin/sh\necho 'table inet filter {'\necho '}'\n", 0o755)
	saved := switcher.SearchPath
	switcher.SearchPath = append([]string{dir}, saved...)
	t.Cleanup(func() { switcher.SearchPath = saved })
}

func call(args ...string) (int, string, string) {
	var out, errOut strings.Builder
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

var ackPattern = regexp.MustCompile(`ah-switch ack --txn ([0-9a-f,]+) --asked WORDS`)

// ackTxn is the --txn of the ack command the output out shows, as the
// owner copies it.
func ackTxn(t *testing.T, out string) string {
	t.Helper()
	m := ackPattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the output shows no ack command:\n%s", out)
	}
	return m[1]
}

func TestUsage(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{},
		{"--root", root},
		{"--root", root, "0001-aaaaaaa", "0002-bbbbbbb"},
		{"--root", root, "--rollback-to", "previous", "0001-aaaaaaa"},
		{"--root", root, "--rollback-to", "latest"},
		{"--root", root, "--mode", "trust", "0001-aaaaaaa"},
		{"--root", root, "--mode", "signed", "0001-aaaaaaa"},
		{"--root", root, "--allowed-signers", "/x", "0001-aaaaaaa"},
		{"--root", root, "7-aaaaaaa"},
		{"--root", root, "--dry", "status"},
		{"--root", root, "--from", "/x", "status"},
		{"--root", root, "--from", "/x", "--rollback-to", "previous"},
		{"--root", root, "--from", "", "0001-aaaaaaa"},
		{"--root", root, "0001-aaaaaaa"},
		{"--root", root, "--rollback-to", "previous"},
		{"--root", root, "--allow-exec", "status"},
		{"--root", root, "recover"},
		{"--root", root, "--allow-exec", "recover", "--counter", "x"},
		{"--root", root, "--allow-exec", "--counter", "3", "0001-aaaaaaa"},
		{"--root", root, "--from", "/x", "recover", "--counter", "3"},
		{"--root", root, "--no-such-flag", "0001-aaaaaaa"},
		{"--root", "", "status"},
	} {
		code, _, errOut := call(args...)
		if code != switcher.ExitUsage || !strings.Contains(errOut, "ah-switch: ") {
			t.Errorf("%q: exit %d, stderr %q", args, code, errOut)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Errorf("a usage error wrote into the root: %v, %v", entries, err)
	}
	if code, out, _ := call("--help"); code != switcher.ExitOK || !strings.Contains(out, "usage: ah-switch") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestStatusOfEmptyRoot(t *testing.T) {
	code, out, errOut := call("status", "--root", t.TempDir())
	if code != switcher.ExitOK || !strings.Contains(out, "current: none") {
		t.Errorf("exit %d, %q, %q", code, out, errOut)
	}
}

func write(t *testing.T, path, content string, perm fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// hostStops writes the host's own stop list, which every switch requires,
// as the one command made of the script prog. The script is copied below
// root as /usr/bin/ah-test-stop, a protected host program of the sandbox
// root, because a host stop list may name a bare program or an absolute
// path of a protected host program, and nothing else.
func hostStops(t *testing.T, root, prog string) {
	t.Helper()
	script, err := os.ReadFile(prog)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "usr/bin/ah-test-stop"), string(script), 0o755)
	write(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "/usr/bin/ah-test-stop\n", 0o644)
}

// releaseIn writes into dir a release whose no-AI check and health command
// are scripts, with its SHA256SUMS: an incoming folder, imported with --from.
// Its lists run its own bin/health, a copy of the script health: a program
// of a release's lists is a file of the release (or a protected host
// program).
func releaseIn(t *testing.T, dir, id string, counter int, health string) {
	t.Helper()
	commit := strings.SplitN(id, "-", 2)[1]
	script, err := os.ReadFile(health)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "MANIFEST"), "id: "+id+"\ncounter: "+strconv.Itoa(counter)+"\ncommit: "+commit+"\nbuilt_on: dev\ngo_version: go\npackage_snapshot: s\n", 0o644)
	write(t, filepath.Join(dir, "bin/ah-noaicheck"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(dir, "bin/ah-pkgstep"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(dir, "bin/health"), string(script), 0o755)
	own := "{release}/bin/health"
	write(t, filepath.Join(dir, "share/health.cmd"), own+"\n", 0o644)
	write(t, filepath.Join(dir, "share/reload.list"), own+"\n", 0o644)
	write(t, filepath.Join(dir, "share/validators.list"), own+"\n", 0o644)
	write(t, filepath.Join(dir, "share/stop.list"), own+"\n", 0o644)
	var lines []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(dir, path)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+rel)
		return nil
	})
	sort.Strings(lines)
	write(t, filepath.Join(dir, "SHA256SUMS"), strings.Join(lines, "\n")+"\n", 0o644)
}

func TestSwitchRollbackAndStatus(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritableAll(root) })
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	hostStops(t, root, health)
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	in := t.TempDir()
	releaseIn(t, filepath.Join(in, "0001-aaaaaaa"), "0001-aaaaaaa", 1, health)

	code, out, errOut := call("--root", root, "--dry", "--from", filepath.Join(in, "0001-aaaaaaa"), "0001-aaaaaaa")
	if code != switcher.ExitOK || !strings.Contains(out, "would point current at releases/0001-aaaaaaa") {
		t.Fatalf("dry: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(root, switcher.StateDir)); err == nil {
		t.Fatalf("--dry made the state folder")
	}
	fakeNFT(t)
	for i, id := range []string{"0001-aaaaaaa", "0002-bbbbbbb"} {
		if i > 0 {
			releaseIn(t, filepath.Join(in, id), id, i+1, health)
		}
		code, out, errOut := call(id, "--root", root, "--allow-exec", "--from", filepath.Join(in, id))
		if code != switcher.ExitOK {
			t.Fatalf("switch to %s: exit %d\n%s%s", id, code, out, errOut)
		}
		txn := ackTxn(t, out)
		// The outcome is owed until the owner acknowledges it.
		code, out, errOut = call("--root", root, "--allow-exec", "0009-fffffff")
		if code != switcher.ExitFailed || !strings.Contains(out, "owed: the switch to "+id) || ackTxn(t, out) != txn {
			t.Errorf("a request while an outcome is owed: exit %d\n%s%s", code, out, errOut)
		}
		if code, out, errOut := call("--root", root, "ack", "--txn", txn, "--asked", "result seen"); code != switcher.ExitOK {
			t.Fatalf("ack: exit %d\n%s%s", code, out, errOut)
		}
	}
	code, out, _ = call("--root", root, "--allow-exec", "0001-aaaaaaa")
	if code != switcher.ExitFailed || !strings.Contains(out, "not above the highest ever attempted") {
		t.Errorf("lower counter: exit %d\n%s", code, out)
	}
	code, out, _ = call("--root", root, "--allow-exec", "--rollback-to", "previous")
	if code != switcher.ExitOK {
		t.Fatalf("rollback: exit %d\n%s", code, out)
	}
	code, out, _ = call("--root", root, "status")
	if code != switcher.ExitOK {
		t.Fatalf("status: exit %d", code)
	}
	for _, want := range []string{"current: 0001-aaaaaaa", "previous: 0002-bbbbbbb", "highest counter: 2", "last switch: rollback to 0001-aaaaaaa: ok", "outcome owed (request rollback:0001-aaaaaaa"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

// TestImportFrom imports a release from an incoming folder given relative to
// the working folder.
func TestImportFrom(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	hostStops(t, root, health)
	incoming := t.TempDir()
	releaseIn(t, filepath.Join(incoming, "0001-aaaaaaa"), "0001-aaaaaaa", 1, health)
	t.Chdir(incoming)
	fakeNFT(t)
	code, out, errOut := call("--root", root, "--allow-exec", "--from", "0001-aaaaaaa", "0001-aaaaaaa")
	if code != switcher.ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	info, err := os.Stat(filepath.Join(root, switcher.BaseDir, "releases", "0001-aaaaaaa", "bin", "ah-noaicheck"))
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Errorf("imported check: %v, %v", info, err)
	}
}

func makeWritableAll(root string) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(path, 0o755)
		}
		return nil
	})
}

// TestNoAICheckOverrideRefusedUnderSudo: --noaicheck PATH replaces the
// release's no-AI check with any protected program of the host, which a
// root-owned program such as /usr/bin/true is. A sudo rule for the switch
// (the deploying user's) must not choose it: run through sudo (SUDO_UID or
// SUDO_USER set) a switch or rollback with --noaicheck is refused before
// anything is looked at; without sudo the same request goes on.
func TestNoAICheckOverrideRefusedUnderSudo(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritableAll(root) })
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	hostStops(t, root, health)
	write(t, filepath.Join(root, "usr/bin/ah-test-check"), "#!/bin/sh\nexit 0\n", 0o755)
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	in := filepath.Join(t.TempDir(), "0001-aaaaaaa")
	releaseIn(t, in, "0001-aaaaaaa", 1, health)
	noSudo := func() {
		for _, v := range []string{"SUDO_UID", "SUDO_USER"} {
			t.Setenv(v, "")
			os.Unsetenv(v)
		}
	}
	switchArgs := []string{"--root", root, "--dry", "--noaicheck", "/usr/bin/ah-test-check", "--from", in, "0001-aaaaaaa"}
	for _, v := range []string{"SUDO_UID", "SUDO_USER"} {
		t.Run(v, func(t *testing.T) {
			noSudo()
			t.Setenv(v, "1000")
			for _, args := range [][]string{switchArgs, {"--root", root, "--dry", "--noaicheck", "/usr/bin/ah-test-check", "--rollback-to", "previous"}} {
				code, out, errOut := call(args...)
				if code != switcher.ExitUsage || !strings.Contains(errOut, "--noaicheck is refused under sudo") || out != "" {
					t.Errorf("%q under sudo: exit %d\n%s%s", args, code, out, errOut)
				}
			}
		})
	}
	t.Run("not under sudo", func(t *testing.T) {
		noSudo()
		code, out, errOut := call(switchArgs...)
		if code != switcher.ExitOK || !strings.Contains(out, "ah-test-check --phase pre") {
			t.Errorf("exit %d\n%s%s", code, out, errOut)
		}
	})
}

// TestRecover writes a missing highest-counter back.
func TestRecover(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeWritableAll(root) })
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	hostStops(t, root, health)
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	in := t.TempDir()
	releaseIn(t, filepath.Join(in, "0001-aaaaaaa"), "0001-aaaaaaa", 1, health)
	releaseIn(t, filepath.Join(in, "0002-bbbbbbb"), "0002-bbbbbbb", 2, health)
	fakeNFT(t)
	code, out, errOut := call("--root", root, "--allow-exec", "--from", filepath.Join(in, "0001-aaaaaaa"), "0001-aaaaaaa")
	if code != switcher.ExitOK {
		t.Fatalf("switch: exit %d\n%s%s", code, out, errOut)
	}
	if code, out, errOut := call("--root", root, "ack", "--txn", ackTxn(t, out), "--asked", "result seen"); code != switcher.ExitOK {
		t.Fatalf("ack: exit %d\n%s%s", code, out, errOut)
	}
	os.Remove(filepath.Join(root, switcher.StateDir, switcher.HighestFile))
	if code, out, _ := call("--root", root, "--allow-exec", "--from", filepath.Join(in, "0002-bbbbbbb"), "0002-bbbbbbb"); code != switcher.ExitFailed || !strings.Contains(out, "recover --counter N") {
		t.Fatalf("switch without a counter: exit %d\n%s", code, out)
	}
	if code, out, _ := call("--root", root, "--allow-exec", "recover", "--counter", "0"); code != switcher.ExitFailed || !strings.Contains(out, "below 1") {
		t.Fatalf("recover below the evidence: exit %d\n%s", code, out)
	}
	if code, out, errOut := call("--root", root, "--allow-exec", "recover", "--counter", "1"); code != switcher.ExitOK {
		t.Fatalf("recover: exit %d\n%s%s", code, out, errOut)
	}
	if code, out, errOut := call("--root", root, "--allow-exec", "--from", filepath.Join(in, "0002-bbbbbbb"), "0002-bbbbbbb"); code != switcher.ExitOK {
		t.Fatalf("switch after recover: exit %d\n%s%s", code, out, errOut)
	}
}

// TestDiagnosticsEscaped: what was typed reaches the diagnostics only
// escaped: an unknown flag, a bad flag value.
func TestDiagnosticsEscaped(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--root", root, "--no\x1b[2J-such", "0001-aaaaaaa"},
		{"--root", root, "--allow-exec", "recover", "--counter", "x\x1b[2J\ny"},
		{"--root", root, "--rollback-to", "previous\x1b[2J"},
		{"--root", root, "--mode", "x\x1b[2J", "0001-aaaaaaa"},
	} {
		code, out, errOut := call(args...)
		if code != switcher.ExitUsage {
			t.Errorf("%q: exit %d", args, code)
		}
		if strings.ContainsAny(out+errOut, "\x1b") {
			t.Errorf("%q: a control byte was passed on: %q", args, errOut)
		}
	}
}

// TestRecoverForms: recover takes exactly one of its forms.
func TestRecoverForms(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--root", root, "recover", "--counter", "3", "--services-stopped"},
		{"--root", root, "recover", "--counter", "3", "--package-state", "s"},
		{"--root", root, "recover", "--package-state", "s", "--services-stopped"},
		{"--root", root, "recover", "--discard-journal", "0123456789abcdef0123456789abcdef", "--services-stopped"},
		{"--root", root, "recover", "--discard-journal", ""},
		{"--root", root, "recover", "--discard-journal", "x"},
		{"--root", root, "--discard-journal", "0123456789abcdef0123456789abcdef", "0001-aaaaaaa"},
		{"--root", root, "--package-state", "s", "0001-aaaaaaa"},
		{"--root", root, "--services-stopped", "0001-aaaaaaa"},
		{"--root", root, "--package-state", "s", "status"},
		{"--root", root, "--noaicheck", "bin/check", "0001-aaaaaaa"},
		{"--root", root, "inhibit-clear"},
		{"--root", root, "inhibit-clear", "--asked", ""},
		{"--root", root, "inhibit-clear", "--asked", "x", "--counter", "3"},
		{"--root", root, "--asked", "x", "0001-aaaaaaa"},
		{"--root", root, "--asked", "x", "status"},
		{"--root", root, "ack"},
		{"--root", root, "ack", "--asked", ""},
		// A valid --txn and --asked, so that each of these can fail only
		// for the flag it names.
		{"--root", root, "ack", "--txn", "0123456789abcdef0123456789abcdef", "--asked", "x", "--allow-exec"},
		{"--root", root, "--rollback-to", "previous", "ack", "--txn", "0123456789abcdef0123456789abcdef", "--asked", "x"},
		{"--root", root, "ack", "--asked", "x"},
		{"--root", root, "ack", "--txn", "", "--asked", "x"},
		{"--root", root, "ack", "--txn", "0123456789abcdef0123456789abcdef", "--asked", ""},
		{"--root", root, "ack", "--txn", "0123456789abcdef0123456789abcdeF", "--asked", "x"},
		{"--root", root, "ack", "--txn", "0123456789abcdef0123456789abcdef,", "--asked", "x"},
		{"--root", root, "ack", "--txn", "0123456789abcdef0123456789abcdef,0123456789abcdef0123456789abcdef", "--asked", "x"},
		{"--root", root, "--txn", "0123456789abcdef0123456789abcdef", "0001-aaaaaaa"},
		{"--root", root, "--txn", "0123456789abcdef0123456789abcdef", "status"},
		{"--root", root, "inhibit-clear", "--asked", "x", "--txn", "0123456789abcdef0123456789abcdef"},
	} {
		if code, _, errOut := call(args...); code != switcher.ExitUsage {
			t.Errorf("%q: exit %d, %q", args, code, errOut)
		}
	}
}

// TestBoot: ah-switch boot takes only --root and --allow-exec; on a root
// where nothing is deployed it exits 4 and creates nothing; on a host
// switched to a healthy release it qualifies current again and exits 0,
// decisions no longer inhibited; on that host with current removed it
// exits 1, decisions inhibited.
func TestBoot(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"--root", root, "boot"},
		{"--root", root, "--allow-exec", "--dry", "boot"},
		{"--root", root, "--allow-exec", "--mode", "manual", "boot"},
		{"--root", root, "--allow-exec", "--from", "/x", "boot"},
		{"--root", root, "--allow-exec", "--noaicheck", "/x", "boot"},
		{"--root", root, "--allow-exec", "--counter", "3", "boot"},
		{"--root", root, "--allow-exec", "--asked", "x", "boot"},
		{"--root", root, "--allow-exec", "--txn", "0123456789abcdef0123456789abcdef", "boot"},
		{"--root", root, "--allow-exec", "--rollback-to", "previous", "boot"},
		{"--root", root, "--allow-exec", "boot", "0001-aaaaaaa"},
	} {
		if code, _, errOut := call(args...); code != switcher.ExitUsage || !strings.Contains(errOut, "ah-switch: ") {
			t.Errorf("%q: exit %d, %q", args, code, errOut)
		}
	}
	code, out, errOut := call("--root", root, "--allow-exec", "boot")
	if code != switcher.ExitNoRelease || !strings.Contains(out, "no current release") {
		t.Errorf("boot of an empty root: exit %d\n%s%s", code, out, errOut)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Errorf("boot of an empty root wrote into it: %v, %v", entries, err)
	}
	if code, out, _ := call("--help"); code != switcher.ExitOK || !strings.Contains(out, "ah-switch [--root DIR [--allow-exec]] boot") {
		t.Errorf("--help does not show boot: exit %d\n%s", code, out)
	}

	root = t.TempDir()
	t.Cleanup(func() { makeWritableAll(root) })
	health := filepath.Join(t.TempDir(), "health")
	write(t, health, "#!/bin/sh\nexit 0\n", 0o755)
	hostStops(t, root, health)
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	in := t.TempDir()
	releaseIn(t, filepath.Join(in, "0001-aaaaaaa"), "0001-aaaaaaa", 1, health)
	fakeNFT(t)
	code, out, errOut = call("0001-aaaaaaa", "--root", root, "--allow-exec", "--from", filepath.Join(in, "0001-aaaaaaa"))
	if code != switcher.ExitOK {
		t.Fatalf("switch: exit %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = call("--root", root, "--allow-exec", "boot")
	if code != switcher.ExitOK || !strings.Contains(out, "result: ok; current -> 0001-aaaaaaa, qualified since the boot") {
		t.Errorf("boot: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(root, switcher.StateDir, switcher.InhibitFile)); err == nil {
		t.Error("decisions are inhibited after a boot that qualified current")
	}
	code, out, _ = call("--root", root, "status")
	if code != switcher.ExitOK || !strings.Contains(out, "decisions: not inhibited") || !strings.Contains(out, "outcome owed (request forward:0001-aaaaaaa") {
		t.Errorf("status after boot: exit %d\n%s", code, out)
	}
	// A deployed host whose current is missing is not one where nothing
	// is deployed: boot inhibits decisions and fails, exit 1.
	if err := os.Remove(filepath.Join(root, switcher.BaseDir, "current")); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = call("--root", root, "--allow-exec", "boot")
	if code != switcher.ExitFailed || !strings.Contains(out, "current is missing") {
		t.Errorf("boot without current on a deployed host: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Lstat(filepath.Join(root, switcher.StateDir, switcher.InhibitFile)); err != nil {
		t.Errorf("decisions are not inhibited after boot found current missing: %v", err)
	}
}
