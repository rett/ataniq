package consoleclient

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
)

// The tests in this file need a listener on a Unix socket, and some of them
// the client protocol's own server, which exists for Linux only; hence the
// file's name. Every socket is a private one in a temporary folder.

// peerMark is in every text a fake peer sends that the package must not
// repeat.
const peerMark = "PEER-TEXT"

// socketDir returns a folder path, not yet created, whose socket path is
// short enough for a Unix socket address.
func socketDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	if len(clientapi.SocketPath(dir)) > 100 {
		base, err := os.MkdirTemp("", "cc")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })
		dir = filepath.Join(base, "run")
	}
	return dir
}

// read calls Read with a deadline far enough away that only a hung test
// meets it.
func read(t *testing.T, dir string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Read(ctx, dir)
}

// handler is a clientapi.Handler with fixed answers.
type handler struct {
	version   string
	status    clientapi.StatusResult
	statusErr error
}

func (h *handler) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: h.version}
}

func (h *handler) Status(context.Context) (clientapi.StatusResult, error) {
	return h.status, h.statusErr
}

// startServer serves h with the client protocol's own server on a private
// socket, until the test ends, and returns the socket's folder.
func startServer(t *testing.T, h clientapi.Handler, opts clientapi.Options) string {
	t.Helper()
	dir := socketDir(t)
	l, _, err := clientapi.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- clientapi.Serve(ctx, l, h, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after its context ended")
		}
	})
	return dir
}

// fakePeer is a listener that speaks the wire by hand, for the peers the real
// server cannot be: slow, hostile, or of another protocol. It records every
// operation it is sent.
type fakePeer struct {
	dir string

	mu     sync.Mutex
	ops    []string
	conns  int
	closed int // connections the client closed
}

// hangUp, returned by a fake's answer function, closes the connection in
// place of an answer. A nil answer writes nothing and keeps the connection
// open until the client closes it.
var hangUp = []byte{}

// startFake listens on a private socket and answers every request line with
// what answer returns for it. When the test ends it fails the test if any
// operation other than hello and status was sent.
func startFake(t *testing.T, answer func(req clientapi.Request) []byte) *fakePeer {
	t.Helper()
	f := &fakePeer{dir: socketDir(t)}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", clientapi.SocketPath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var connsMu sync.Mutex
	var conns []net.Conn
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, c)
			connsMu.Unlock()
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				f.serve(c, answer)
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		connsMu.Lock()
		for _, c := range conns {
			c.Close()
		}
		connsMu.Unlock()
		wg.Wait()
		for _, op := range f.sent() {
			if op != clientapi.OpHello && op != clientapi.OpStatus {
				t.Errorf("the operation %q was sent", op)
			}
		}
	})
	return f
}

func (f *fakePeer) serve(c net.Conn, answer func(req clientapi.Request) []byte) {
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			f.mu.Lock()
			f.closed++
			f.mu.Unlock()
			return
		}
		var req clientapi.Request
		if json.Unmarshal(line, &req) != nil {
			req.Op = "unreadable: " + string(line)
		}
		f.mu.Lock()
		f.ops = append(f.ops, req.Op)
		f.mu.Unlock()
		out := answer(req)
		if out == nil {
			continue
		}
		if len(out) == 0 {
			return
		}
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

// sent returns the operations received so far, in order.
func (f *fakePeer) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// okLine is a success response to req.
func okLine(t *testing.T, req clientapi.Request, result any) []byte {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return respLine(t, clientapi.Response{Protocol: clientapi.Protocol, RequestID: req.RequestID, OK: true, Result: raw})
}

// errLine is an error response to req. Its message carries peerMark.
func errLine(t *testing.T, req clientapi.Request, code clientapi.ErrorCode, protocols ...string) []byte {
	t.Helper()
	return respLine(t, clientapi.Response{Protocol: clientapi.Protocol, RequestID: req.RequestID,
		Error: &clientapi.ErrorBody{Code: code, Message: peerMark + " \x1b[2J", SupportedProtocols: protocols}})
}

func respLine(t *testing.T, resp clientapi.Response) []byte {
	t.Helper()
	line, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

// goodHello is the hello result of a peer that speaks the protocol.
func goodHello() clientapi.HelloResult {
	return clientapi.HelloResult{Protocols: clientapi.SupportedProtocols(), Operations: clientapi.Operations(), Version: "0.0.0-test"}
}

// stringsOf returns every string held in v, with the path to each.
func stringsOf(v any) map[string]string {
	out := make(map[string]string)
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		switch v.Kind() {
		case reflect.String:
			out[path] = v.String()
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		}
	}
	walk("Result", reflect.ValueOf(v))
	return out
}

// wantState asserts that r is the state want with that state's fixed, one-line
// text and next step, holds no view, and repeats nothing a fake peer sent.
func wantState(t *testing.T, r Result, want State) {
	t.Helper()
	if r.State != want {
		t.Fatalf("state = %q (%q), want %q", r.State, r.Text, want)
	}
	texts, ok := stateTexts[want]
	if !ok {
		t.Fatalf("no texts for the state %q", want)
	}
	if r.Text != texts[0] || r.NextStep != texts[1] || r.Text == "" || r.NextStep == "" {
		t.Errorf("text = %q, next step = %q, want the fixed texts %q", r.Text, r.NextStep, texts)
	}
	if !reflect.DeepEqual(r.View, View{}) {
		t.Errorf("a result without a view holds one: %+v", r.View)
	}
	for path, s := range stringsOf(r) {
		if strings.Contains(s, peerMark) || strings.Contains(s, "clientapi") || strings.ContainsAny(s, "\x1b\n") {
			t.Errorf("%s = %q repeats the peer or an error", path, s)
		}
	}
}

func TestEveryStateHasAOneLineTextAndNextStep(t *testing.T) {
	states := []State{StateNoDaemon, StateSlow, StateOtherProtocol, StateRefused, StateBadAnswer, StateUnavailable}
	if len(stateTexts) != len(states) {
		t.Errorf("%d states have texts, want %d", len(stateTexts), len(states))
	}
	for _, s := range states {
		r := state(s)
		wantState(t, r, s)
		for _, text := range []string{r.Text, r.NextStep} {
			if shownSafely(text) != "" || len(text) > 80 {
				t.Errorf("%s: %q is not one short, plain line", s, text)
			}
		}
	}
	if _, ok := stateTexts[StateOK]; ok {
		t.Error("the state with a view has a text")
	}
}

func TestNoSocket(t *testing.T) {
	missing := socketDir(t) // the folder does not exist
	empty := socketDir(t)
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	// A socket file that nothing listens on: an earlier daemon's.
	stale := socketDir(t)
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", clientapi.SocketPath(stale))
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Stat(clientapi.SocketPath(stale)); err != nil {
		t.Fatalf("the stale socket file is gone: %v", err)
	}
	for name, dir := range map[string]string{"no folder": missing, "an empty folder": empty, "a stale socket": stale} {
		r := read(t, dir)
		wantState(t, r, StateNoDaemon)
		// The next step names the command as the daemon's guide does.
		if !strings.Contains(r.NextStep, "ataniqd run") {
			t.Errorf("%s: next step %q does not name the command", name, r.NextStep)
		}
	}
}

func TestSlowPeer(t *testing.T) {
	for _, name := range []string{"never answers hello", "never answers status"} {
		f := startFake(t, func(req clientapi.Request) []byte {
			if req.Op == clientapi.OpHello && name == "never answers status" {
				return okLine(t, req, goodHello())
			}
			return nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		start := time.Now()
		r := Read(ctx, f.dir)
		cancel()
		wantState(t, r, StateSlow)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("%s: Read took %v past a deadline of 150ms", name, took)
		}
	}
	// A deadline that has passed before the call.
	f := startFake(t, func(clientapi.Request) []byte { return nil })
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	wantState(t, Read(ctx, f.dir), StateSlow)
}

// A context without a deadline is still bounded: Read gives it the default
// timeout. The test lowers the default; no test of the package runs in
// parallel with it.
func TestNoDeadlineGetsTheDefaultTimeout(t *testing.T) {
	if defaultTimeout != DefaultTimeout || DefaultTimeout <= 0 {
		t.Fatalf("Read's default is %v, want DefaultTimeout (%v)", defaultTimeout, DefaultTimeout)
	}
	defaultTimeout = 150 * time.Millisecond
	t.Cleanup(func() { defaultTimeout = DefaultTimeout })
	f := startFake(t, func(clientapi.Request) []byte { return nil })
	done := make(chan Result, 1)
	go func() { done <- Read(context.Background(), f.dir) }()
	select {
	case r := <-done:
		wantState(t, r, StateSlow)
	case <-time.After(5 * time.Second):
		// Left running, Read ends when the test's cleanup closes the fake.
		t.Fatal("Read with a context without a deadline did not return")
	}
}

func TestCancelledCaller(t *testing.T) {
	f := startFake(t, func(clientapi.Request) []byte { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantState(t, Read(ctx, f.dir), StateUnavailable)
}

func TestOtherProtocol(t *testing.T) {
	t.Run("the peer answers unsupported-protocol", func(t *testing.T) {
		f := startFake(t, func(req clientapi.Request) []byte {
			return errLine(t, req, clientapi.CodeUnsupportedProtocol, peerMark+"/9")
		})
		wantState(t, read(t, f.dir), StateOtherProtocol)
	})
	t.Run("hello lists other protocols only", func(t *testing.T) {
		f := startFake(t, func(req clientapi.Request) []byte {
			h := goodHello()
			h.Protocols = []string{peerMark + "/9", "ataniq.client/10"}
			return okLine(t, req, h)
		})
		wantState(t, read(t, f.dir), StateOtherProtocol)
		if got := f.sent(); !reflect.DeepEqual(got, []string{clientapi.OpHello}) {
			t.Errorf("sent %q, want hello alone", got)
		}
	})
	t.Run("the peer does not offer status", func(t *testing.T) {
		f := startFake(t, func(req clientapi.Request) []byte {
			if req.Op == clientapi.OpHello {
				return okLine(t, req, goodHello())
			}
			return errLine(t, req, clientapi.CodeUnsupportedOperation)
		})
		wantState(t, read(t, f.dir), StateOtherProtocol)
	})
}

func TestRefused(t *testing.T) {
	// The real server, told that the peer is another user.
	dir := startServer(t, &handler{}, clientapi.Options{
		PeerUID: func(net.Conn) (uint32, error) { return uint32(os.Geteuid()) + 1, nil },
	})
	wantState(t, read(t, dir), StateRefused)

	// A fake that words its refusal with text of its own.
	f := startFake(t, func(req clientapi.Request) []byte {
		req.RequestID = ""
		return errLine(t, req, clientapi.CodeUnauthorized)
	})
	wantState(t, read(t, f.dir), StateRefused)
}

func TestBadAnswer(t *testing.T) {
	overCap := append([]byte(strings.Repeat("a", clientapi.MaxResponseBytes+1)), '\n')
	for name, answer := range map[string]func(t *testing.T, req clientapi.Request) []byte{
		"over the cap": func(t *testing.T, req clientapi.Request) []byte { return overCap },
		"status over the cap": func(t *testing.T, req clientapi.Request) []byte {
			if req.Op == clientapi.OpHello {
				return okLine(t, req, goodHello())
			}
			return overCap
		},
		"not JSON":  func(t *testing.T, req clientapi.Request) []byte { return []byte(peerMark + " \x1b[2J not json\n") },
		"not UTF-8": func(t *testing.T, req clientapi.Request) []byte { return []byte("{\"protocol\":\"\xff\"}\n") },
		"hangs up":  func(t *testing.T, req clientapi.Request) []byte { return hangUp },
		"half a line": func(t *testing.T, req clientapi.Request) []byte {
			// Written, then the fake's next read ends when the client closes.
			return []byte(`{"protocol":"ataniq.client/1",`)
		},
		"another request id": func(t *testing.T, req clientapi.Request) []byte {
			req.RequestID = peerMark
			return okLine(t, req, goodHello())
		},
		"another protocol name on the answer": func(t *testing.T, req clientapi.Request) []byte {
			return respLine(t, clientapi.Response{Protocol: peerMark + "/2", RequestID: req.RequestID, OK: true, Result: json.RawMessage(`{}`)})
		},
		"a result of another shape": func(t *testing.T, req clientapi.Request) []byte { return okLine(t, req, peerMark) },
		"a status of another shape": func(t *testing.T, req clientapi.Request) []byte {
			if req.Op == clientapi.OpHello {
				return okLine(t, req, goodHello())
			}
			return okLine(t, req, map[string]any{"leads": peerMark})
		},
		"a code no request of ours earns": func(t *testing.T, req clientapi.Request) []byte {
			return errLine(t, req, clientapi.CodeInvalidRequest)
		},
		"a code nobody knows": func(t *testing.T, req clientapi.Request) []byte {
			return errLine(t, req, clientapi.ErrorCode(peerMark+"\x1b[2J"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := startFake(t, func(req clientapi.Request) []byte { return answer(t, req) })
			wait := 10 * time.Second
			if name == "half a line" {
				wait = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			r := Read(ctx, f.dir)
			if name == "half a line" {
				// The line never ends, so the wait for it runs out.
				wantState(t, r, StateSlow)
				return
			}
			wantState(t, r, StateBadAnswer)
			// A daemon of another version answers like this too (it writes
			// its own protocol's name on the answer), so the next step
			// names that cause and the command.
			for _, part := range []string{"same version", "ataniqd status"} {
				if !strings.Contains(r.NextStep, part) {
					t.Errorf("next step %q does not say %q", r.NextStep, part)
				}
			}
		})
	}
}

func TestUnavailable(t *testing.T) {
	// The real server, whose handler fails: it answers "internal".
	dir := startServer(t, &handler{statusErr: errors.New(peerMark)}, clientapi.Options{})
	wantState(t, read(t, dir), StateUnavailable)

	f := startFake(t, func(req clientapi.Request) []byte {
		req.RequestID = ""
		return errLine(t, req, clientapi.CodeBusy)
	})
	wantState(t, read(t, f.dir), StateUnavailable)

	// A folder that may not be searched. Root may search it all the same.
	if os.Geteuid() != 0 {
		locked := startFake(t, func(clientapi.Request) []byte { return nil }).dir
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o700)
		wantState(t, read(t, locked), StateUnavailable)
	}
}

// shownStates are the shown states of the client protocol.
var shownStates = []string{
	clientapi.StateDesired, clientapi.StateStarting, clientapi.StateReady, clientapi.StateDegraded, clientapi.StateStalled,
	clientapi.StateStopping, clientapi.StateStopped, clientapi.StateEnded, clientapi.StateUnknown,
}

func TestEntryWordsAreTheProtocols(t *testing.T) {
	for _, s := range shownStates {
		if got := entryState(s); string(got) != s {
			t.Errorf("entryState(%q) = %q", s, got)
		}
	}
	for _, s := range []string{"", "READY", "ready ", peerMark, "\x1b[31mready"} {
		if got := entryState(s); got != EntryUnknown {
			t.Errorf("entryState(%q) = %q, want unknown", s, got)
		}
	}
	if KindLead != clientapi.KindLead || KindSupervisor != clientapi.KindSupervisor || KindCoordinator != clientapi.KindCoordinator {
		t.Error("a kind is not worded as the protocol words it")
	}
}

func TestViewFromTheServer(t *testing.T) {
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	one := uint64(1)
	profile := clientapi.RequestedProfile{Provider: "provider-a", Version: "1", Model: "model-a", Effort: "high"}
	status := clientapi.StatusResult{
		Daemon: clientapi.DaemonStatus{PID: 4242, StartedAt: started, RegistryRevision: 6, LastReconcileCompleted: true},
		Leads: []clientapi.LeadStatus{
			{Role: "demo/lead", Kind: clientapi.KindLead, Project: "demo", Manager: "ataniq", Epoch: 3, Desired: "running",
				State: clientapi.StateReady, ReasonCode: "observed", ReasonDetail: "up", Since: since, Requested: profile},
			{Role: "supervisor", Kind: clientapi.KindSupervisor, Manager: "ataniq", Epoch: 1, Desired: "running",
				State: clientapi.StateDegraded, ReasonCode: "probe", Since: since},
		},
	}
	// A coordinator in every shown state, and a lead of its own project in
	// every state a lead shows.
	for i, s := range shownStates {
		feature := fmt.Sprintf("feature-%d", i)
		inst := uint64(i)
		status.Leads = append(status.Leads, clientapi.LeadStatus{
			Role: "demo/coord/" + feature, Kind: clientapi.KindCoordinator, Project: "demo", Manager: "ataniq", Epoch: inst,
			Desired: "running", State: s, ReasonCode: "reason-" + s, Since: since,
			Requested: clientapi.RequestedProfile{Provider: "runtime-a", Model: "model-a"},
			Feature:   feature, Parent: "demo/lead", ParentEpoch: &one, Instance: &inst,
			Identity: fmt.Sprintf("demo-lead@e1:coord:%s:%d", feature, i),
		})
		if s != clientapi.StateStalled {
			status.Leads = append(status.Leads, clientapi.LeadStatus{
				Role: "p-" + s + "/lead", Kind: clientapi.KindLead, Project: "p-" + s, State: s, Since: since})
		}
	}
	dir := startServer(t, &handler{version: "0.0.0-test", status: status}, clientapi.Options{})

	// A context without a deadline is served within the default one.
	r := Read(context.Background(), dir)
	if r.State != StateOK || r.Text != "" || r.NextStep != "" {
		t.Fatalf("result = %q %q %q, want a view", r.State, r.Text, r.NextStep)
	}
	v := r.View
	if want := (Daemon{Version: "0.0.0-test", PID: 4242, StartedAt: started, ReconcileCompleted: true}); v.Daemon != want {
		t.Errorf("daemon = %+v, want %+v", v.Daemon, want)
	}
	// Scratch status identifies projects and roles by their raw wire strings.
	// It carries no home or conversation ID.
	wantID := func(raw string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) }
	wantSupervisor := Entry{ID: wantID("supervisor"), Kind: KindSupervisor, Name: "supervisor", State: EntryDegraded, Desired: "running",
		Reason: "probe", Since: since, Epoch: 1, Manager: "ataniq"}
	if !v.HasSupervisor || v.Supervisor != wantSupervisor {
		t.Errorf("supervisor = %v %+v, want %+v", v.HasSupervisor, v.Supervisor, wantSupervisor)
	}
	if v.LeftOut != 0 {
		t.Errorf("left out %d entries, want none", v.LeftOut)
	}
	if len(v.Projects) != len(shownStates) { // demo, and one per lead state
		t.Fatalf("%d projects, want %d", len(v.Projects), len(shownStates))
	}
	demo := v.Projects[0]
	wantLead := Entry{ID: wantID("demo/lead"), Kind: KindLead, Name: "demo/lead", Project: "demo", State: EntryReady, Desired: "running",
		Reason: "observed", Detail: "up", Since: since, Epoch: 3, Manager: "ataniq",
		Provider: "provider-a", Model: "model-a", Effort: "high"}
	if demo.Name != "demo" || !demo.HasLead || demo.Lead != wantLead {
		t.Errorf("project = %q, lead = %v %+v, want %+v", demo.Name, demo.HasLead, demo.Lead, wantLead)
	}
	if demo.HomeID != "" || demo.ID != wantID("demo") {
		t.Errorf("demo project identity = (%q, %q), want local project %q", demo.HomeID, demo.ID, wantID("demo"))
	}
	if len(demo.Coordinators) != len(shownStates) {
		t.Fatalf("%d coordinators, want %d", len(demo.Coordinators), len(shownStates))
	}
	for i, s := range shownStates {
		feature := fmt.Sprintf("feature-%d", i)
		role := "demo/coord/" + feature
		want := Entry{ID: wantID(role), Kind: KindCoordinator, Name: role, Project: "demo", Feature: feature,
			Parent: "demo/lead", State: EntryState(s), Desired: "running", Reason: "reason-" + s, Since: since,
			Epoch: uint64(i), Manager: "ataniq", Provider: "runtime-a", Model: "model-a"}
		if got := demo.Coordinators[i]; got != want {
			t.Errorf("coordinator %d = %+v, want %+v", i, got, want)
		}
	}
	seen := map[EntryState]bool{}
	for _, p := range v.Projects[1:] {
		project := "p-" + string(p.Lead.State)
		if p.HomeID != "" || p.ID != wantID(project) || p.Lead.ID != wantID(project+"/lead") || p.Lead.ConversationID != "" {
			t.Errorf("project %q: unexpected scratch identity in %+v", project, p)
		}
		if !p.HasLead || len(p.Coordinators) != 0 || p.Lead.Project != p.Name || p.Name != "p-"+string(p.Lead.State) {
			t.Errorf("project %q: lead %+v, %d coordinators", p.Name, p.Lead, len(p.Coordinators))
		}
		seen[p.Lead.State] = true
	}
	if len(seen) != len(shownStates)-1 {
		t.Errorf("leads in %d states, want %d", len(seen), len(shownStates)-1)
	}
}

// hostile returns a string that holds tag between an escape sequence and
// direction overrides, a bell, a line break, text over the length bound and a
// title sequence.
func hostile(tag string) string {
	return "\x1b[31m" + tag + "\u202e\u2066\u200b\x07\r\n\u009b2J" + strings.Repeat("W", maxInspect+1000) + "\x1b]0;" + peerMark + "\x07"
}

func TestHostileStringsComeOutCleanAndBounded(t *testing.T) {
	entry := func(kind, project string) clientapi.LeadStatus {
		u := uint64(1)
		return clientapi.LeadStatus{
			Role: hostile("role"), Kind: kind, Project: project, Manager: hostile("manager"), Epoch: 1,
			Desired: hostile("desired"), State: hostile("ready"), ReasonCode: hostile("reason"),
			ReasonDetail: hostile("detail"), EvidenceKind: hostile("evidence"),
			Requested: clientapi.RequestedProfile{Provider: hostile("provider"), Version: hostile("version"), Model: hostile("model"),
				Effort: hostile("effort"), PermissionMode: hostile("permission"), AccessMode: hostile("access")},
			Observed:     clientapi.ObservedProfile{Provider: clientapi.Observe(hostile("observed")), Model: clientapi.Observe(hostile("observed"))},
			LaunchIntent: &clientapi.LaunchIntent{ID: hostile("intent"), Unit: hostile("unit"), Outcome: hostile("outcome")},
			Feature:      hostile("feature"), Parent: hostile("parent"), ParentEpoch: &u, Instance: &u, Identity: hostile("identity"),
		}
	}
	status := clientapi.StatusResult{
		Daemon: clientapi.DaemonStatus{PID: -7, BootID: hostile("boot")},
		Leads: []clientapi.LeadStatus{
			entry(clientapi.KindLead, hostile("project")),
			entry(clientapi.KindSupervisor, hostile("project")),
			entry(clientapi.KindCoordinator, hostile("project")),
			entry(hostile("lead"), hostile("project")), // a kind nobody knows
		},
	}
	dir := startServer(t, &handler{version: hostile("version"), status: status}, clientapi.Options{})
	r := read(t, dir)
	if r.State != StateOK {
		t.Fatalf("state = %q, want a view", r.State)
	}
	v := r.View
	if !v.HasSupervisor || len(v.Projects) != 1 || !v.Projects[0].HasLead || len(v.Projects[0].Coordinators) != 1 || v.LeftOut != 1 {
		t.Fatalf("view = supervisor %v, %d projects, left out %d", v.HasSupervisor, len(v.Projects), v.LeftOut)
	}
	if v.Daemon.PID != 0 {
		t.Errorf("pid = %d, want 0 for a number that is no process ID", v.Daemon.PID)
	}
	all := stringsOf(r)
	// Every string field of the view was fed a hostile value, apart from the
	// kinds and states, which are words of this package.
	filled := 0
	for path, s := range all {
		if why := shownSafely(s); why != "" {
			t.Errorf("%s = %q holds %s", path, s, why)
		}
		if strings.Contains(s, peerMark) {
			t.Errorf("%s holds the end of an over-long string", path)
		}
		if strings.HasPrefix(s, "[31m") {
			filled++
			if !strings.HasSuffix(s, cutMark) {
				t.Errorf("%s = %q was cut without the mark", path, s)
			}
		}
	}
	// The daemon's version, the project's name, and per entry: name, desired,
	// reason, detail, manager, provider, model, effort; the project (not the
	// supervisor's); the feature and the parent (the coordinator's).
	if want := 2 + 3*8 + 2 + 2; filled != want {
		t.Errorf("%d strings carry a cleaned hostile value, want %d: %v", filled, want, all)
	}
	lead, coord := v.Projects[0].Lead, v.Projects[0].Coordinators[0]
	if !strings.HasPrefix(lead.Name, "[31mrole  2JWWW") {
		t.Errorf("lead name = %q", lead.Name)
	}
	for _, e := range []Entry{lead, coord, v.Supervisor} {
		if e.State != EntryUnknown {
			t.Errorf("%s: state = %q, want unknown for a word that is no state", e.Kind, e.State)
		}
	}
	if v.Supervisor.Project != "" || v.Supervisor.Feature != "" || lead.Feature != "" || lead.Parent != "" {
		t.Errorf("a field of another kind is set: supervisor %+v, lead %+v", v.Supervisor, lead)
	}
}

// An entry of a kind the package does not know, and a lead without a
// project, make no project and are counted as left out. Each is first to
// name its project here, in a status far below the bounds, so that no other
// rule leaves it out.
func TestEntriesWithoutAKindOrAProjectMakeNoProject(t *testing.T) {
	status := clientapi.StatusResult{Leads: []clientapi.LeadStatus{
		{Role: "/lead", Kind: clientapi.KindLead, Project: "", State: clientapi.StateReady},
		{Role: "solo/operator", Kind: "operator", Project: "solo", State: clientapi.StateReady},
		{Role: "/coord", Kind: clientapi.KindCoordinator, Project: "", State: clientapi.StateReady},
		{Role: "demo/lead", Kind: clientapi.KindLead, Project: "demo", State: clientapi.StateReady},
	}}
	dir := startServer(t, &handler{status: status}, clientapi.Options{})
	r := read(t, dir)
	if r.State != StateOK {
		t.Fatalf("state = %q, want a view", r.State)
	}
	v := r.View
	if len(v.Projects) != 1 || v.Projects[0].Name != "demo" || v.Projects[0].Lead.Name != "demo/lead" {
		t.Errorf("projects = %+v, want the demo project alone", v.Projects)
	}
	if v.LeftOut != 3 || v.HasSupervisor {
		t.Errorf("left out %d, supervisor %v, want 3 and none", v.LeftOut, v.HasSupervisor)
	}
}

func TestCountsAreBounded(t *testing.T) {
	var leads []clientapi.LeadStatus
	add := func(kind, project, role string) {
		leads = append(leads, clientapi.LeadStatus{Role: role, Kind: kind, Project: project, State: clientapi.StateReady})
	}
	const projects, coordinators = MaxProjects + 8, MaxCoordinators + 6
	for i := 0; i < projects; i++ {
		add(clientapi.KindLead, fmt.Sprintf("p%d", i), fmt.Sprintf("p%d/lead", i))
	}
	for i := 0; i < coordinators; i++ {
		add(clientapi.KindCoordinator, "p0", fmt.Sprintf("p0/coord/f%d", i))
	}
	add(clientapi.KindSupervisor, "", "supervisor")
	// Each of these is left out.
	add(clientapi.KindSupervisor, "", "supervisor")        // a second supervisor
	add(clientapi.KindLead, "p1", "p1/lead")               // a second lead
	add(clientapi.KindLead, "", "/lead")                   // no project
	add(clientapi.KindCoordinator, "\x1b\u202e", "x")      // a project that cleans to nothing
	add("operator", "p1", "p1/operator")                   // a kind nobody knows
	add(clientapi.KindCoordinator, "late", "late/coord/f") // a project past the bound
	const others = 6

	dir := startServer(t, &handler{status: clientapi.StatusResult{Leads: leads}}, clientapi.Options{})
	r := read(t, dir)
	if r.State != StateOK {
		t.Fatalf("state = %q, want a view", r.State)
	}
	v := r.View
	if len(v.Projects) != MaxProjects {
		t.Errorf("%d projects, want %d", len(v.Projects), MaxProjects)
	}
	if got := len(v.Projects[0].Coordinators); got != MaxCoordinators {
		t.Errorf("%d coordinators, want %d", got, MaxCoordinators)
	}
	if v.Projects[1].Lead.Name != "p1/lead" || len(v.Projects[1].Coordinators) != 0 {
		t.Errorf("project 1 = %+v", v.Projects[1])
	}
	if want := (projects - MaxProjects) + (coordinators - MaxCoordinators) + others; v.LeftOut != want {
		t.Errorf("left out %d, want %d", v.LeftOut, want)
	}
	shown := 1 + MaxProjects + MaxCoordinators
	if shown+v.LeftOut != len(leads) {
		t.Errorf("%d shown and %d left out do not add up to %d", shown, v.LeftOut, len(leads))
	}
}

// Two projects whose names clean to the same text stay two projects: the
// second one's lead is not left out as a second lead of the first, and each
// keeps its own coordinator.
func TestProjectsThatShowAlikeStayApart(t *testing.T) {
	const hidden = "a\u200b" // shows as "a"
	status := clientapi.StatusResult{Leads: []clientapi.LeadStatus{
		{Role: "first/lead", Kind: clientapi.KindLead, Project: hidden, State: clientapi.StateReady},
		{Role: "second/lead", Kind: clientapi.KindLead, Project: "a", State: clientapi.StateReady},
		{Role: "second/coord", Kind: clientapi.KindCoordinator, Project: "a", State: clientapi.StateReady},
		{Role: "first/coord", Kind: clientapi.KindCoordinator, Project: hidden, State: clientapi.StateReady},
	}}
	dir := startServer(t, &handler{status: status}, clientapi.Options{})
	r := read(t, dir)
	if r.State != StateOK {
		t.Fatalf("state = %q, want a view", r.State)
	}
	v := r.View
	if len(v.Projects) != 2 || v.LeftOut != 0 {
		t.Fatalf("%d projects, %d left out, want 2 and 0: %+v", len(v.Projects), v.LeftOut, v.Projects)
	}
	for i, want := range []string{"first", "second"} {
		p := v.Projects[i]
		if p.Name != "a" || !p.HasLead || p.Lead.Name != want+"/lead" || p.Lead.Project != "a" ||
			len(p.Coordinators) != 1 || p.Coordinators[0].Name != want+"/coord" {
			t.Errorf("project %d = %+v, want the %s project shown as \"a\"", i, p, want)
		}
	}
}

func TestSendsHelloAndStatusAndNothingElse(t *testing.T) {
	status := clientapi.StatusResult{Leads: []clientapi.LeadStatus{
		{Role: "demo/lead", Kind: clientapi.KindLead, Project: "demo", State: clientapi.StateReady},
	}}
	f := startFake(t, func(req clientapi.Request) []byte {
		switch req.Op {
		case clientapi.OpHello:
			return okLine(t, req, goodHello())
		case clientapi.OpStatus:
			return okLine(t, req, status)
		}
		return errLine(t, req, clientapi.CodeUnsupportedOperation)
	})
	for i := 0; i < 3; i++ {
		r := read(t, f.dir)
		if r.State != StateOK || len(r.View.Projects) != 1 || r.View.Daemon.Version != "0.0.0-test" {
			t.Fatalf("read %d = %+v", i, r)
		}
	}
	want := []string{"hello", "status", "hello", "status", "hello", "status"}
	if got := f.sent(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
	// Read closes what it opened: one connection per call, each closed by the
	// client by the time the call returned or shortly after.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		conns, closed := f.conns, f.closed
		f.mu.Unlock()
		if conns == 3 && closed == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections, %d closed by the client, want 3 and 3", conns, closed)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
