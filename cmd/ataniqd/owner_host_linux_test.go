//go:build linux && ataniq_ownerchannel

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
	"ataniq/internal/leaddaemon"
)

// cmdSuite is a suite runner that is never asked: these tests build the
// service and read; they ask for no verification.
type cmdSuite struct{}

func (cmdSuite) Run(context.Context, hostadapter.SuiteRequest) (hostadapter.SuiteResult, error) {
	return hostadapter.SuiteResult{}, nil
}

// scratchSettings writes the scratch settings of a state folder, whose owner
// folder is private, and returns the state folder, the mirror's path and the
// drop folder.
func scratchSettings(t *testing.T, mutate func(*hostadapter.Scratch)) (state, drop string) {
	t.Helper()
	base := shortDir(t)
	state, drop = base+"/s", base+"/drop"
	for _, d := range []string{state, state + "/owner", drop} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sc := hostadapter.Scratch{
		Project: "project-a", Mirror: base + "/mirror.git", Role: "role-a", Target: "refs/heads/develop",
		Identity: hostadapter.ScratchIdentity{Name: "Host Identity", Email: "host@example.invalid"},
		Gates:    []hostadapter.ScratchGate{{Name: "suite", Argv: []string{"make", "test"}}},
		Drop:     drop, Seat: hostadapter.ScratchSeat{Epoch: 7, SeatRev: 12},
	}
	if mutate != nil {
		mutate(&sc)
	}
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "owner", hostadapter.ScratchName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return state, drop
}

// useSuite puts a suite runner in place of the build's, which has none.
func useSuite(t *testing.T, s hostadapter.Suite) {
	t.Helper()
	old := ownerSuite
	ownerSuite = s
	t.Cleanup(func() { ownerSuite = old })
}

// The command's one place that builds the host's side of a card is the
// builder the build registers by itself: the daemon of a tagged build has it
// before any test replaces it.
func TestTheCommandRegistersTheHostAdapterAsTheBuilder(t *testing.T) {
	// Every test that replaces the builder restores it, so this is what the
	// binary has.
	if ownerServiceBuilder == nil {
		t.Fatal("no builder is registered")
	}
	if got, want := reflect.ValueOf(ownerServiceBuilder).Pointer(), reflect.ValueOf(buildOwnerService).Pointer(); got != want {
		t.Fatalf("the registered builder is not the host adapter's")
	}
}

// Without a suite runner the build refuses, with words that name what is
// missing, before it opens anything.
func TestTheBuilderRefusesWithoutASuiteRunner(t *testing.T) {
	useSuite(t, nil)
	state, _ := scratchSettings(t, nil)
	svc, closer, err := buildOwnerService(state)
	if err == nil || svc != nil || closer != nil || !strings.Contains(err.Error(), "no verification suite") {
		t.Fatalf("buildOwnerService = %v, %v, %v", svc, closer != nil, err)
	}
	if _, err := os.Lstat(filepath.Join(state, "owner", hostadapter.JournalName)); err == nil {
		t.Errorf("a journal was made for a build that refused")
	}
}

// Without settings, or with settings that do not hold, it refuses, and it
// opens nothing.
func TestTheBuilderRefusesMissingAndBadSettings(t *testing.T) {
	useSuite(t, cmdSuite{})
	base := shortDir(t)
	if err := os.Mkdir(base+"/s", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildOwnerService(base + "/s"); err == nil {
		t.Errorf("a state folder with no owner folder built a service")
	}
	for name, mutate := range map[string]func(*hostadapter.Scratch){
		"a relative drop folder": func(s *hostadapter.Scratch) { s.Drop = "drop" },
		"no gate":                func(s *hostadapter.Scratch) { s.Gates = nil },
		"a bad project name":     func(s *hostadapter.Scratch) { s.Project = "Bad Name" },
		"a target that is not a branch": func(s *hostadapter.Scratch) {
			s.Target = "refs/tags/v1"
		},
		"a negative epoch": func(s *hostadapter.Scratch) { s.Seat.Epoch = -1 },
	} {
		state, _ := scratchSettings(t, mutate)
		if _, _, err := buildOwnerService(state); err == nil {
			t.Errorf("%s: a service was built", name)
		}
		if _, err := os.Lstat(filepath.Join(state, "owner", hostadapter.JournalName)); err == nil {
			t.Errorf("%s: a journal was made", name)
		}
	}
	// A settings file that others can read, a key that is not known and text
	// after the object are refused.
	state, _ := scratchSettings(t, nil)
	path := filepath.Join(state, "owner", hostadapter.ScratchName)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildOwnerService(state); err == nil {
		t.Errorf("a settings file the group and others can read was accepted")
	}
	for name, content := range map[string]string{
		"an unknown key":     strings.Replace(string(good), `"project"`, `"extra":1,"project"`, 1),
		"text after":         string(good) + `{}`,
		"not an object":      `[]`,
		"an empty file":      ``,
		"a number for a key": strings.Replace(string(good), `"role":"role-a"`, `"role":7`, 1),
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := buildOwnerService(state); err == nil {
			t.Errorf("%s: a service was built", name)
		}
	}
}

// The service is built in the state folder: the journal is the real store in
// its owner folder, closing ends it and frees the journal, and a second build
// on the same state folder works after that.
func TestTheBuilderBuildsTheServiceInTheStateFolder(t *testing.T) {
	useSuite(t, cmdSuite{})
	state, _ := scratchSettings(t, nil)
	svc, closer, err := buildOwnerService(state)
	if err != nil {
		t.Fatalf("buildOwnerService: %v", err)
	}
	l, err := svc.List(context.Background())
	if err != nil || len(l.Pending) != 0 {
		t.Fatalf("List = %+v, %v", l, err)
	}
	jpath := filepath.Join(state, "owner", hostadapter.JournalName)
	st, err := os.Stat(jpath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the journal: %v %v", st, err)
	}
	if _, err := ownerjournal.Open(jpath); err == nil {
		t.Fatalf("the journal was opened a second time while the service holds it")
	}
	if err := closer(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closer(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	j, err := ownerjournal.Open(jpath)
	if err != nil {
		t.Fatalf("the journal is still held after the close: %v", err)
	}
	_ = j.Close()
	svc2, closer2, err := buildOwnerService(state)
	if err != nil {
		t.Fatalf("a second build: %v", err)
	}
	_ = svc2
	if err := closer2(); err != nil {
		t.Fatal(err)
	}
}

// The builder starts the collector on the drop folder of the settings: a
// request file with a feature name outside the grammar is answered with a
// reply file, with no daemon and no mirror to read.
func TestTheBuilderStartsTheCollectorOnTheDropFolder(t *testing.T) {
	useSuite(t, cmdSuite{})
	state, drop := scratchSettings(t, nil)
	_, closer, err := buildOwnerService(state)
	if err != nil {
		t.Fatal(err)
	}
	// A request that is no request: it gets a reply, which shows that the
	// collector scans the folder without being asked.
	if err := os.WriteFile(filepath.Join(drop, "rq-cmd-01.request"), []byte("not a request"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply := filepath.Join(drop, "rq-cmd-01.reply")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(reply); err == nil {
			if !strings.Contains(string(b), `"code":"request-invalid"`) {
				t.Errorf("reply: %s", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reply file in 10 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	// After the close nothing scans: a second file stays where it is.
	if err := os.WriteFile(filepath.Join(drop, "rq-cmd-02.request"), []byte("not a request"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * hostadapter.DefaultScanEvery / 2)
	if _, err := os.Stat(filepath.Join(drop, "rq-cmd-02.reply")); err == nil {
		t.Errorf("the collector went on after the close")
	}
}

// The daemon, with the flag and the host adapter as its builder, serves the
// owner operations, reads the empty list from the real service, and frees the
// journal when it stops.
func TestOwnerRunWithTheHostAdapterServesTheOwnerOperations(t *testing.T) {
	useSuite(t, cmdSuite{})
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	state, _ := scratchSettings(t, nil)
	runtime := filepath.Dir(state) + "/r"
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	done := startApp(t, ta, []string{"--state-dir", state, "--runtime-dir", runtime, "--owner-channel"}, runtime+"/daemon.sock")

	h := ownerCmdHello(t, runtime+"/daemon.sock")
	want := append([]string{"hello", "status"}, ownerwire.Operations()...)
	if strings.Join(h.Operations, " ") != strings.Join(want, " ") {
		t.Fatalf("operations %v", h.Operations)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, runtime+"/daemon.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var l ownerwire.Listing
	if err := c.Call(ctx, ownerwire.OpList, nil, &l); err != nil || len(l.Pending) != 0 {
		t.Fatalf("owner.list: %+v, %v", l, err)
	}
	var dr ownerwire.DeciderResult
	if err := c.Call(ctx, ownerwire.OpDecider, ownerwire.DeciderParams{Client: "ataniq-console 0.0.11", Session: "0123456789abcdef"}, &dr); err != nil || !dr.Decider {
		t.Fatalf("owner.decider: %+v, %v", dr, err)
	}
	c.Close()
	ta.signals <- syscall.SIGTERM
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	j, err := ownerjournal.Open(filepath.Join(state, "owner", hostadapter.JournalName))
	if err != nil {
		t.Fatalf("the journal is held after the daemon stopped: %v", err)
	}
	_ = j.Close()
}

// A daemon whose build has no suite runner refuses to serve the owner channel
// and says why, with the serve step's exit code, and makes no socket.
func TestOwnerRunWithoutASuiteRunnerStopsTheDaemon(t *testing.T) {
	useSuite(t, nil)
	state, _ := scratchSettings(t, nil)
	runtime := filepath.Dir(state) + "/r"
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
	if code != leaddaemon.ExitServe || !strings.Contains(ta.err.String(), "no verification suite") {
		t.Fatalf("exit %d: %q", code, ta.err.String())
	}
}
