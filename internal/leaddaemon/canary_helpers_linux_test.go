//go:build linux && ataniq_canary

package leaddaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// canaryRoleText is the role of the slice.
const canaryRoleText = "throwaway-one/lead"

// canaryEnv is a daemon in canary mode with the in-memory unit manager and a
// fake control server of the holder.
type canaryEnv struct {
	*env
	cfg       leadcanary.Config
	holderDir string
	holder    *fakeHolder
	running   *running
	ids       int
	// build, when set, is the daemon's check of the holder's build; unset
	// it finds every holder the expected build (sameHolderBuild).
	build *buildid.Checker
}

// noEnv is a Getenv with nothing set: the account's own defaults still count.
func noEnv(string) string { return "" }

// newCanaryEnv makes the folders of a canary run (the state root with its
// marker, the runtime root and the holder's folder) and a config for them. The
// fake manager reads the system clock, so that a stop needs no clock driver.
func newCanaryEnv(t *testing.T) *canaryEnv {
	t.Helper()
	e := newEnv(t)
	e.fake = leadunit.NewFake(leadunit.SystemClock{})
	role, err := leadrole.Parse(canaryRoleText)
	if err != nil {
		t.Fatal(err)
	}
	holderDir := filepath.Join(e.runtime, "leads", "throwaway-one-lead")
	// What provisioning makes and canary.start checks: the runtime folder, the
	// holder's state folder and the folder of the role lock. (Made by hand
	// here, over an in-memory unit manager; the tests of cmd/ataniq-holder that
	// run on a tree provisioning made take these folders from provisioning.)
	holderState := filepath.Join(e.state, "leads", "throwaway-one-lead", "holder")
	for _, d := range []string{
		e.state, e.runtime, filepath.Join(e.runtime, "leads"), holderDir,
		filepath.Join(e.state, "leads"), filepath.Dir(holderState), holderState,
		filepath.Join(e.state, "roles"), filepath.Join(e.state, "roles", "throwaway-one"),
	} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := canaryroot.WriteMarker(e.state, role.Project()); err != nil {
		t.Fatal(err)
	}
	c := &canaryEnv{env: e, holderDir: holderDir}
	c.cfg = leadcanary.Config{
		Version: 1, Role: role, StateRoot: e.state, RuntimeRoot: e.runtime,
		Holder: leadcanary.HolderConfig{
			Executable: "/opt/sample/holder", Cols: 120, Rows: 40,
			SandboxInputs: filepath.Join(e.base, "inputs.json"), ProfileDigest: strings.Repeat("ab", 32),
		},
		Agent: leadcanary.AgentConfig{
			Kind:           leadcanary.AgentStandIn,
			Runtime:        string(leadregistry.DefaultCoordinatorProvider), // a key of the registry's runtime enum
			RuntimeVersion: "1.2.3", FooterMode: "sample mode on",
			Argv: []string{"/opt/sample/agent", "--flag"}, Env: []string{"SAMPLE_ONE=1"},
		},
		Inject: true,
		Limits: leadcanary.Limits{
			MemoryMaxBytes: 4 << 30, SwapMaxBytes: 0, TasksMax: 512,
			MemoryFloorBytes: 1 << 30, MemorySharePercent: 50,
		},
		Provision: leadcanary.ProvisionConfig{
			Mirror: filepath.Join(e.base, "mirror.git"), UpstreamBranch: "main",
			GitName: "Sample Name", GitEmail: "sample@example.invalid",
			ManagedSettings: "/opt/sample/managed.json", GuardHook: "/opt/sample/guard",
			ReplayScreens: "/opt/sample/screens",
		},
	}
	return c
}

// daemonConfig is the daemon's config for the canary config of c.
func (c *canaryEnv) daemonConfig() Config {
	cfg := c.env.config()
	cfg.Clock = leadunit.SystemClock{}
	build := c.build
	if build == nil {
		build = sameHolderBuild()
	}
	cfg.Canary = &Canary{
		Config: c.cfg, Getenv: noEnv,
		InjectWait: 300 * time.Millisecond, DialTimeout: 2 * time.Second,
		HolderBuild: build,
	}
	return cfg
}

// start runs the daemon in canary mode.
func (c *canaryEnv) start() {
	c.t.Helper()
	c.running = c.startDaemon(c.daemonConfig())
}

// call makes one call of op over the client socket and decodes the result.
func (c *canaryEnv) call(op string, params, result any) error {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, c.running.socket)
	if err != nil {
		c.t.Fatal(err)
	}
	defer cl.Close()
	return cl.Call(ctx, op, params, result)
}

// mustCall is call, and the call must succeed.
func (c *canaryEnv) mustCall(op string, params, result any) {
	c.t.Helper()
	if err := c.call(op, params, result); err != nil {
		c.t.Fatalf("%s: %v", op, err)
	}
}

// wantCode checks that err is the daemon's error with the code and returns it.
func wantCode(t *testing.T, err error, code clientapi.ErrorCode) *clientapi.Error {
	t.Helper()
	ce, ok := err.(*clientapi.Error)
	if !ok || ce.Code != code {
		t.Fatalf("error %v, want the code %q", err, code)
	}
	return ce
}

func (c *canaryEnv) role() CanaryParams { return CanaryParams{Role: canaryRoleText} }

func (c *canaryEnv) define() CanaryDefineResult {
	c.t.Helper()
	var r CanaryDefineResult
	c.mustCall(OpCanaryDefine, c.role(), &r)
	return r
}

func (c *canaryEnv) startRole() CanaryStartResult {
	c.t.Helper()
	var r CanaryStartResult
	c.mustCall(OpCanaryStart, c.role(), &r)
	return r
}

func (c *canaryEnv) stopRole() CanaryStopResult {
	c.t.Helper()
	var r CanaryStopResult
	c.mustCall(OpCanaryStop, c.role(), &r)
	return r
}

// inject sends a message; a fresh id is made when id is empty.
func (c *canaryEnv) inject(id, body string) (CanaryInjectResult, error) {
	c.t.Helper()
	var r CanaryInjectResult
	err := c.call(OpCanaryInject, CanaryInjectParams{Role: canaryRoleText, MessageID: id, Body: body}, &r)
	return r, err
}

// up starts the daemon, starts the fake holder and brings the role up.
func (c *canaryEnv) up(answer holderAnswer) CanaryStartResult {
	c.t.Helper()
	c.start()
	c.holder = newFakeHolder(c.t, c.holderDir, answer)
	c.define()
	return c.startRole()
}

// evidence returns the lines of the evidence file.
func (c *canaryEnv) evidence() []map[string]any {
	c.t.Helper()
	f, err := os.Open(c.cfg.EvidencePath())
	if err != nil {
		c.t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			c.t.Fatalf("evidence line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// events returns the event names of the evidence, in order.
func (c *canaryEnv) events() []string {
	c.t.Helper()
	var out []string
	for _, m := range c.evidence() {
		out = append(out, m["event"].(string))
	}
	return out
}

// lastEvidence returns the last line of the evidence.
func (c *canaryEnv) lastEvidence() map[string]any {
	c.t.Helper()
	all := c.evidence()
	if len(all) == 0 {
		c.t.Fatal("the evidence is empty")
	}
	return all[len(all)-1]
}

// entry returns the registry's record of the role, from the daemon's status.
func (c *canaryEnv) shown() (state, reason string, epoch uint64, found bool) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, c.running.socket)
	if err != nil {
		c.t.Fatal(err)
	}
	defer cl.Close()
	st, err := cl.Status(ctx)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, l := range st.Leads {
		if l.Role == canaryRoleText {
			return l.State, l.ReasonCode, l.Epoch, true
		}
	}
	return "", "", 0, false
}

// holderAnswer decides what the fake holder does with one control request.
type holderAnswer func(req wire.Request, conn net.Conn)

// respondWith answers every request with resp.
func respondWith(resp wire.Response) holderAnswer {
	return func(_ wire.Request, conn net.Conn) { writeResponse(conn, resp) }
}

// submitted is the answer of a holder that delivered the message; a ticket
// request is answered ok.
func submitted(req wire.Request, conn net.Conn) {
	if req.Op == wire.OpInject {
		writeResponse(conn, wire.Response{OK: true, Inject: &wire.InjectResult{
			Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "sample detail",
		}})
		return
	}
	writeResponse(conn, wire.Response{OK: true})
}

// silent reads the request and never answers; the connection ends when the
// daemon closes it.
func silent(_ wire.Request, conn net.Conn) { io.Copy(io.Discard, conn) }

// closeAtOnce closes the connection after the request.
func closeAtOnce(_ wire.Request, conn net.Conn) { conn.Close() }

// garbage answers with bytes that are no frame.
func garbage(_ wire.Request, conn net.Conn) {
	conn.Write([]byte("not a frame at all, only a lot of bytes\n"))
}

func writeResponse(conn net.Conn, resp wire.Response) {
	p, err := wire.EncodeResponse(resp)
	if err != nil {
		panic(err)
	}
	_ = wire.Write(conn, wire.ControlResponses, wire.TypeResponse, p)
}

// fakeHolder serves the holder's control socket in a folder and records the
// requests.
type fakeHolder struct {
	ln     net.Listener
	answer holderAnswer
	wg     sync.WaitGroup

	mu   sync.Mutex
	reqs []wire.Request
}

func newFakeHolder(t *testing.T, dir string, answer holderAnswer) *fakeHolder {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, controlSocketName))
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHolder{ln: ln, answer: answer}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(30 * time.Second))
				f, err := wire.NewReader(conn, wire.ControlRequests).Next()
				if err != nil {
					return
				}
				req, err := wire.DecodeRequest(f.Payload)
				if err != nil {
					return
				}
				h.mu.Lock()
				h.reqs = append(h.reqs, req)
				h.mu.Unlock()
				h.answer(req, conn)
			}()
		}
	}()
	t.Cleanup(h.close)
	return h
}

func (h *fakeHolder) close() {
	h.ln.Close()
	h.wg.Wait()
}

// requests returns the requests seen, of op when op is not empty.
func (h *fakeHolder) requests(op wire.Operation) []wire.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []wire.Request
	for _, r := range h.reqs {
		if op == "" || r.Op == op {
			out = append(out, r)
		}
	}
	return out
}
