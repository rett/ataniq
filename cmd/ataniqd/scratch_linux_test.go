//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// shortTB gives a scratch world a short temporary folder: the sockets in it must
// fit the address of a Unix socket.
type shortTB struct {
	testing.TB
	dir string
}

func (s shortTB) TempDir() string { return s.dir }

func newScratchWorld(t *testing.T) *provisiontest.ScratchWorld {
	t.Helper()
	return provisiontest.NewScratch(shortTB{TB: t, dir: shortDir(t)})
}

// scratchApp is a test app whose environment points the check of the default
// roots into the world.
func scratchApp(t *testing.T, w *provisiontest.ScratchWorld) *testApp {
	t.Helper()
	ta := canaryApp(t)
	ta.getenv = w.Getenv()
	return ta
}

func TestUsageNamesTheScratchCommand(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	got := ta.out.String()
	for _, want := range []string{
		"ataniqd run --scratch CONFIG", "run --scratch CONFIG", "lead.list", "lead.inject", "serves no canary operation",
		"--state-dir, --runtime-dir", "scratch-init",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
	// The release text and the line that limits it still open the usage.
	override := "In this build (tag ataniq_canary) the text above holds only without --canary."
	if !strings.HasPrefix(got, baseUsage+"\n"+override+"\n") {
		t.Errorf("the usage does not open with the release text and the line that limits it")
	}
}

// --scratch excludes --canary, --state-dir and --runtime-dir, and needs a config:
// a usage error, before anything is read or made.
func TestRunScratchRefusesTheOtherFlags(t *testing.T) {
	w := newScratchWorld(t)
	cf := newCanaryFixture(t)
	for _, args := range [][]string{
		{"run", "--scratch", w.ConfigPath, "--state-dir", w.State},
		{"run", "--scratch", w.ConfigPath, "--runtime-dir", w.Runtime},
		{"run", "--state-dir", w.State, "--scratch", w.ConfigPath},
		{"run", "--scratch", w.ConfigPath, "--canary", cf.cfgPath},
		{"run", "--canary", cf.cfgPath, "--scratch", w.ConfigPath},
		{"run", "--scratch", ""},
		{"run", "--scratch"},
	} {
		ta := scratchApp(t, w)
		if code := ta.run(args); code != leaddaemon.ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, leaddaemon.ExitUsage, ta.err.String())
		}
	}
	if _, err := os.Lstat(w.Runtime); err == nil {
		t.Fatal("a daemon made the runtime root")
	}
	// The canary's own refusal of the root flags is as it was.
	ta := canaryApp(t)
	if code := ta.run([]string{"run", "--canary", cf.cfgPath, "--state-dir", cf.state}); code != leaddaemon.ExitUsage {
		t.Fatalf("canary with --state-dir: %d", code)
	}
}

// A scratch config that cannot be read or is not right is refused at the config
// exit code, and nothing is made.
func TestRunScratchRefusesAConfigThatIsNotRight(t *testing.T) {
	w := newScratchWorld(t)
	loose := filepath.Join(w.Conf, "loose.json")
	if err := os.WriteFile(loose, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	incomplete := filepath.Join(w.Conf, "incomplete.json")
	if err := os.WriteFile(incomplete, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ path, want string }{
		"no such file":    {filepath.Join(w.Conf, "none.json"), "cannot open"},
		"a loose mode":    {loose, "mode"},
		"keys are absent": {incomplete, "invalid config"},
	} {
		t.Run(name, func(t *testing.T) {
			ta := scratchApp(t, w)
			code := ta.run([]string{"run", "--scratch", tc.path})
			if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), tc.want) {
				t.Fatalf("exit %d, stderr %q", code, ta.err.String())
			}
			if _, err := os.Lstat(w.Runtime); err == nil {
				t.Fatal("a daemon made the runtime root")
			}
		})
	}
}

// The daemon refuses a scratch place that has no root marker, and one that is a
// default root of the account; it makes nothing.
func TestRunScratchRefusesAPlaceThatIsNotTheOwners(t *testing.T) {
	w := newScratchWorld(t)
	ta := scratchApp(t, w)
	// The default state folder of the account is the config's root.
	xdg := filepath.Dir(w.State)
	ta.getenv = func(k string) string {
		if k == "XDG_STATE_HOME" {
			return xdg
		}
		return w.Getenv()(k)
	}
	// The root is named so that it is what the default folder resolves to.
	ataniqState := filepath.Join(xdg, "ataniq")
	if err := os.Rename(w.State, ataniqState); err != nil {
		t.Fatal(err)
	}
	w.Config.StateRoot = ataniqState
	w.WriteConfig(t)
	if code := ta.run([]string{"run", "--scratch", w.ConfigPath}); code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "default-root") {
		t.Fatalf("exit %d, stderr %q", code, ta.err.String())
	}

	w2 := newScratchWorld(t)
	if err := os.Remove(filepath.Join(w2.State, canaryroot.ScratchMarkerName)); err != nil {
		t.Fatal(err)
	}
	ta = scratchApp(t, w2)
	if code := ta.run([]string{"run", "--scratch", w2.ConfigPath}); code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "no-marker") {
		t.Fatalf("exit %d, stderr %q", code, ta.err.String())
	}
	if _, err := os.Lstat(w2.Runtime); err == nil {
		t.Fatal("a daemon made the runtime root")
	}
	if entries, err := os.ReadDir(w2.State); err != nil || len(entries) != 0 {
		t.Fatalf("the state root holds %v (%v), want nothing", entries, err)
	}
}

// The command runs the daemon in scratch mode end to end: hello lists the six
// lead operations, the desk's two chat operations and none of the canary's, a
// define provisions and defines a lead of the config, the list shows it, and
// SIGTERM ends the daemon clean.
func TestRunScratchEndToEnd(t *testing.T) {
	w := newScratchWorld(t)
	ta := scratchApp(t, w)
	socket := filepath.Join(w.Runtime, "daemon.sock")
	done := startApp(t, ta, []string{"--scratch", w.ConfigPath}, socket)

	ops, err := helloOperations(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(clientapi.Operations(), leadwire.Operations()...), leadwire.ChatOperations()...)
	if strings.Join(ops, " ") != strings.Join(want, " ") {
		t.Fatalf("hello operations %v, want %v", ops, want)
	}

	ctx := context.Background()
	cl, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var r leadwire.Receipt
	if err := cl.Call(ctx, leadwire.OpDefine, leadwire.DefineParams{Client: "test", CommandID: "def-1", Role: "scratch-alpha/lead"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != leadwire.StatusDone || r.Op != leadwire.OpDefine || r.Role != "scratch-alpha/lead" {
		t.Fatalf("define: %+v", r)
	}
	var list leadwire.ListResult
	if err := cl.Call(ctx, leadwire.OpList, nil, &list); err != nil {
		t.Fatal(err)
	}
	if list.MaxRunning != 2 || len(list.Leads) != 2 || !list.Leads[0].Defined || list.Leads[0].Provision != leadwire.ProvisionDone ||
		list.Leads[1].Defined || list.Leads[1].Provision != leadwire.ProvisionNone {
		t.Fatalf("list: %+v", list)
	}
	// The canary's operations are not served.
	err = cl.Call(ctx, leaddaemon.OpCanaryDefine, map[string]string{"role": "scratch-alpha/lead"}, nil)
	if ce, ok := err.(*clientapi.Error); !ok || ce.Code != clientapi.CodeUnsupportedOperation {
		t.Fatalf("canary.define: %v", err)
	}

	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
}

func TestScratchInstanceInitialization(t *testing.T) {
	w := newScratchWorld(t)
	ta := scratchApp(t, w)
	// A pre-identity scratch root has its provisioning marker and may already
	// hold lead history. Run must leave both intact until explicit init.
	regDir := filepath.Join(w.State, leaddaemon.RegistryDirName)
	r, err := leadregistry.Open(regDir, leadregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(regDir, leadregistry.FileName)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--scratch", w.ConfigPath}
	if code := ta.run(args); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), "ataniqd init --state-dir '"+w.State+"'") {
		t.Fatalf("pre-identity root: exit %d: %s", code, ta.err.String())
	}
	if _, err := instanceid.Load(w.State); !errors.Is(err, instanceid.ErrMissing) {
		t.Fatalf("key: %v", err)
	}
	if after, err := os.ReadFile(journal); err != nil || !bytes.Equal(after, before) {
		t.Fatal("journal changed on refusal")
	}
	if _, err := os.Stat(w.Runtime); !os.IsNotExist(err) {
		t.Fatal("run touched runtime before init")
	}
	if code := ta.run([]string{"init", "--state-dir", w.State, "--label", "sample desk"}); code != 0 {
		t.Fatalf("init: %d: %s", code, ta.err.String())
	}
	id, err := instanceid.Load(w.State)
	if err != nil {
		t.Fatal(err)
	}
	if want := "authority=" + id.Authority + "\nincarnation=" + id.Incarnation + "\nlabel=sample desk\n"; ta.out.String() != want {
		t.Fatalf("init output: %q", ta.out.String())
	}
	// Stop at the manager factory to exercise actual scratch startup through
	// registry replay without binding a socket or starting a unit.
	ta.newManager = func(context.Context) (leadunit.Manager, error) { return nil, errors.New("test manager reached") }
	if code := ta.run(args); code != leaddaemon.ExitManager || !strings.Contains(ta.err.String(), "test manager reached") {
		t.Fatalf("migrated run: %d: %s", code, ta.err.String())
	}
	if err := os.WriteFile(filepath.Join(w.State, instanceid.FileName), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := ta.runConfig(args[1:])
	if err := ta.requireInstance(cfg); !errors.Is(err, instanceid.ErrInvalid) || leaddaemon.ExitCode(err) != leaddaemon.ExitState {
		t.Fatalf("damaged key: %v", err)
	}
	if code := ta.run(args); code != leaddaemon.ExitState {
		t.Fatalf("damaged key: exit %d", code)
	}
}
