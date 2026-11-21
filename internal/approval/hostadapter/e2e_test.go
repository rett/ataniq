//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
	"ataniq/internal/consoleclient"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadunit"
	"ataniq/internal/terminalconsole/approvals"
)

// The tests of this file and of e2e_oc_test.go join every part of the owner
// channel that a host can run without a seated lead: the console's approvals
// model (where a case needs the keys), the console's client, the daemon with
// its owner connection on a private socket, the service, the real journal,
// the host adapter, and a temporary bare mirror read and written by the real
// git. The planner's door is the stand-in (doortest) and the verification
// suite is a fake: no sandbox and no unit is started, and nothing leaves the
// temporary folders.
//
// Every test here runs against a stand-in door that is kinder than the real
// one. A case that passes proves the host's side of the case; each is run
// again at step 2b on a seated lead, against the real door. Where a case
// cannot be shown at all without a seat (a sandboxed lead, the real broker,
// a restored store), its test says so in its comment and shows the part that
// can be shown.
//
// The names of the tests hold OwnerChannel so that
//
//	go test -tags ataniq_ownerchannel ./internal/approval/hostadapter/ -run OwnerChannel
//
// runs them.

// e2eToken is the plan token the stand-in writes into every plan of these
// tests. It is not the stand-in's sample value, so that a search for it finds
// nothing but itself.
const e2eToken = "h-e2e-7d31b0c5"

// e2eClient is the client string of the console in these tests: the first
// word is the name the daemon grants the decider's place to.
const e2eClient = "ataniq-console 0.0.1"

// ttyInspector stands for the process look-up of the daemon: every peer is a
// console with a terminal, as the console on the owner's seat is.
type ttyInspector struct{}

func (ttyInspector) Inspect(leaddaemon.PeerRef) (leaddaemon.PeerProcess, error) {
	return leaddaemon.PeerProcess{TTY: "/dev/pts/4"}, nil
}

// lockedBuffer is a log writer that the test reads while the daemon writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// joined is one test's whole world: the rig's mirror, stand-in door, fake
// suite, clock and folders, with the daemon serving the owner channel on a
// private socket and a console client on it.
type joined struct {
	*rig

	base    string // short, so that the socket path fits
	runtime string
	dstate  string
	logs    *lockedBuffer
	fake    *leadunit.Fake
	oc      *leaddaemon.OwnerChannel

	fmu   sync.Mutex
	fhook func(point string) error
	shook func(ctx context.Context, req SuiteRequest) (SuiteResult, error)

	mu      sync.Mutex
	cancel  context.CancelFunc
	ended   chan struct{}
	runErr  error
	started int
	built   int
	// openErr is what the last build of the service ended with.
	openErr error
}

// newJoined makes the world with the default scene, whose feature side is
// change (nil adds feature.txt), and starts the daemon.
func newJoined(t *testing.T, change func(l *lab)) *joined {
	t.Helper()
	return newJoinedScene(t, func(l *lab) scene { return newScene(l, change) })
}

// newJoinedScene is newJoined with a scene of the test's own making.
func newJoinedScene(t *testing.T, mk func(l *lab) scene) *joined {
	t.Helper()
	l := newLab(t)
	r := &rig{t: t, lab: l, scene: mk(l), clock: doortest.NewClock(start), suite: newSuite(), proj: testProject(l)}
	cfg := doortest.Sample(r.clock.Now)
	cfg.Command = "qargi"
	cfg.PlanToken = e2eToken
	r.stand = doortest.New(cfg)
	r.state = privateDir(t)
	r.drop = privateDir(t)
	base, err := os.MkdirTemp("", "e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	j := &joined{rig: r, base: base, runtime: filepath.Join(base, "run"), dstate: filepath.Join(base, "state"), logs: &lockedBuffer{}}
	clk := leadunit.NewFakeClock(start)
	j.fake = leadunit.NewFake(clk)
	j.oc = &leaddaemon.OwnerChannel{
		Build: j.build, Inspector: ttyInspector{},
		Log: func(f string, a ...any) { _, _ = fmt.Fprintf(j.logs, f+"\n", a...) },
	}
	j.startDaemon()
	return j
}

// setFault sets the hook that the service calls between two steps; a non-nil
// answer stops the service there, as a crash would.
func (j *joined) setFault(h func(point string) error) {
	j.fmu.Lock()
	j.fhook = h
	j.fmu.Unlock()
}

// hookedSuite is the suite of the tests while a hook of the test stands in
// for it.
type hookedSuite struct{ j *joined }

func (h hookedSuite) Run(ctx context.Context, req SuiteRequest) (SuiteResult, error) {
	h.j.fmu.Lock()
	hook := h.j.shook
	h.j.fmu.Unlock()
	if hook == nil {
		return h.j.suite.Run(ctx, req)
	}
	return hook(ctx, req)
}

// suiteHook sets the function that stands for the suite; nil gives the
// fake suite back.
func (j *joined) suiteHook(h func(ctx context.Context, req SuiteRequest) (SuiteResult, error)) {
	j.fmu.Lock()
	j.shook = h
	j.fmu.Unlock()
}

// config is the rig's, with a fault hook that the test may change while the
// daemon runs.
func (j *joined) config() OpenConfig {
	c := j.rig.config()
	c.Suite = hookedSuite{j}
	c.Service.Fault = func(point string) error {
		j.fmu.Lock()
		h := j.fhook
		j.fmu.Unlock()
		if h == nil {
			return nil
		}
		return h(point)
	}
	return c
}

// build is the daemon's builder of the service: the host adapter opened on
// the rig's state folder, door, mirror and drop folder, and its closer. A new
// start of the daemon opens it again on the same folders, as a restart does.
func (j *joined) build() (leaddaemon.OwnerService, func() error, error) {
	o, err := Open(context.Background(), j.config())
	j.mu.Lock()
	j.openErr = err
	if err == nil {
		j.o = o
		j.built++
	}
	j.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return o.Service, o.Close, nil
}

// startDaemon runs the daemon, which builds the service, and waits until it
// listens.
func (j *joined) startDaemon() {
	j.t.Helper()
	clk := leadunit.NewFakeClock(start)
	cfg := leaddaemon.Config{
		StateDir: j.dstate, RuntimeDir: j.runtime,
		NewManager: func(context.Context) (leadunit.Manager, error) { return j.fake, nil },
		Clock:      clk,
		EUID:       os.Geteuid,
		BootID:     func() (string, error) { return j.fake.BootID(), nil },
		Version:    "9.9.9-test",
		Log:        j.logs,
		ServeHook:  j.oc.Serve,
	}
	ready := make(chan string, 1)
	cfg.Ready = func(s string) { ready <- s }
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	j.mu.Lock()
	j.cancel, j.ended, j.runErr = cancel, ended, nil
	j.started++
	j.mu.Unlock()
	go func() {
		err := leaddaemon.Run(ctx, cfg)
		j.mu.Lock()
		j.runErr = err
		j.mu.Unlock()
		close(ended)
	}()
	select {
	case <-ready:
		// The service is built just after the daemon listens.
		wait(j.t, "the service to be built", func() bool {
			j.mu.Lock()
			defer j.mu.Unlock()
			return j.built == j.started || j.runErr != nil
		})
	case <-ended:
		cancel()
		j.t.Fatalf("the daemon ended before it listened: %v\nlog:\n%s", j.runResult(), j.logs)
	case <-time.After(30 * time.Second):
		cancel()
		j.t.Fatalf("the daemon did not listen in 30 s\nlog:\n%s", j.logs)
	}
	j.t.Cleanup(j.stopDaemon)
}

func (j *joined) runResult() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.runErr
}

// stopDaemon ends the daemon, and with it the service, the journal and every
// connection. It may be called twice.
func (j *joined) stopDaemon() {
	j.mu.Lock()
	cancel, ended := j.cancel, j.ended
	j.cancel = nil
	j.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		j.t.Error("the daemon did not stop in 30 s")
	}
}

// restartDaemon is a stop and a start on the same folders.
func (j *joined) restartDaemon() {
	j.t.Helper()
	j.stopDaemon()
	j.startDaemon()
}

// console returns a client of the owner channel, the console's, which dials
// when it is first asked.
func (j *joined) console() *consoleclient.Owner {
	j.t.Helper()
	o, err := consoleclient.NewOwner(consoleclient.OwnerConfig{Dir: j.runtime, Client: e2eClient, Timeout: 30 * time.Second})
	if err != nil {
		j.t.Fatal(err)
	}
	j.t.Cleanup(func() { _ = o.Close() })
	return o
}

// decider returns a console client that holds the decider's place.
func (j *joined) decider() *consoleclient.Owner {
	j.t.Helper()
	o := j.console()
	if ok, why := o.Decider(); !ok {
		j.t.Fatalf("the console is not the decider: %s", why)
	}
	return o
}

// wire is a raw connection to the daemon, for the frames the console never
// sends.
type wire struct {
	t *testing.T
	c *clientapi.Client
}

// dialWire connects to the daemon and, when decider is true, asks for the
// decider's place under the console's name.
func (j *joined) dialWire(decider bool) *wire {
	j.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, clientapi.SocketPath(j.runtime))
	if err != nil {
		j.t.Fatal(err)
	}
	j.t.Cleanup(func() { _ = c.Close() })
	w := &wire{t: j.t, c: c}
	if decider {
		// The place may still be held by a connection that was just
		// closed: ask until the daemon has seen it go.
		wait(j.t, "the decider's place", func() bool {
			var res ownerwire.DeciderResult
			err := w.call(ownerwire.OpDecider, ownerwire.DeciderParams{Client: e2eClient, Session: "0123456789abcdef"}, &res)
			if err != nil {
				j.t.Fatalf("owner.decider: %v", err)
			}
			return res.Decider
		})
	}
	return w
}

func (w *wire) call(op string, params, result any) error {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return w.c.Call(ctx, op, params, result)
}

// beat tells the daemon that the owner is active.
func (w *wire) beat() {
	w.t.Helper()
	var none json.RawMessage
	if err := w.call(ownerwire.OpBeat, nil, &none); err != nil {
		w.t.Fatalf("owner.beat: %v", err)
	}
}

// decide sends a decision frame as it is, and returns the result and the
// error with its code.
func (w *wire) decide(f ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	w.t.Helper()
	var res ownerwire.DecideResult
	err := w.call(ownerwire.OpDecide, f, &res)
	return res, err
}

// codeOf returns the code of a refusal the daemon answered with, or "".
func codeOf(err error) string {
	var ce *clientapi.Error
	if errors.As(err, &ce) {
		return string(ce.Code)
	}
	if c, ok := record.CodeOf(err); ok {
		return string(c)
	}
	return ""
}

// wantRefused fails the test unless err is a refusal with the code.
func wantRefused(t *testing.T, err error, code record.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("no refusal, want %s", code)
	}
	if got := codeOf(err); got != string(code) {
		t.Fatalf("the refusal is %q (%v), want %s", got, err, code)
	}
}

// reading is a Reading of a card that was read to its end, every page of a
// cut path list included, as the console does it, with every flagged path
// signed off when sign is true, opened a second ago.
func (j *joined) reading(o *consoleclient.Owner, c card.Card, sign bool) card.Reading {
	j.t.Helper()
	r := card.Reading{Opened: j.clock.Now().Add(-time.Second), Paths: true, Differs: true}
	for k := range r.Lists {
		r.Lists[k] = true
	}
	r.Refs = true
	if c.Diff != nil && c.Diff.Truncated {
		for n := 0; ; n++ {
			pg, err := o.Page(c.Title.ID, card.PagePaths, n)
			if err != nil {
				j.t.Fatalf("page %d of the path list: %v", n, err)
			}
			r.ReadPage(pg)
			if n+1 >= pg.Pages {
				break
			}
		}
	}
	if sign {
		for _, f := range c.Flagged {
			r.Toggle(f.SignOff())
		}
	}
	return r
}

// approval is the owner's decision on the card that the console would make:
// MayApprove has said yes.
func (j *joined) approval(o *consoleclient.Owner, id string) (card.Card, card.Decision) {
	j.t.Helper()
	c, err := o.Open(id)
	if err != nil {
		j.t.Fatalf("Open(%s): %v", id, err)
	}
	d, ok, why := c.Approve(j.reading(o, c, true), j.clock.Now())
	if !ok {
		j.t.Fatalf("the card cannot be approved: %s", why)
	}
	return c, d
}

// approve opens the card, approves it as the console would and sends the
// decision over o.
func (j *joined) approve(o *consoleclient.Owner, id string) (card.Outcome, error) {
	j.t.Helper()
	_, d := j.approval(o, id)
	o.Activity(j.clock.Now())
	return o.Decide(d)
}

// pendingIDs lists the ids of the pending cards, as the console's client
// sees them.
func (j *joined) pendingIDs(o *consoleclient.Owner) []string {
	j.t.Helper()
	l, err := o.List()
	if err != nil {
		j.t.Fatalf("List: %v", err)
	}
	var ids []string
	for _, it := range l.Pending {
		ids = append(ids, it.ID)
	}
	return ids
}

// wait runs fn until it holds, for up to 30 s.
func wait(t *testing.T, what string, fn func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if fn() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// journalEntries reads the journal of the state folder. The daemon holds the
// journal, so it is stopped first; a test that needs the daemon after this
// starts it again.
func (j *joined) journalEntries() []ownerjournal.Entry {
	j.t.Helper()
	j.stopDaemon()
	jr, err := ownerjournal.Open(filepath.Join(j.state, OwnerDirName, JournalName))
	if err != nil {
		j.t.Fatalf("the journal: %v", err)
	}
	defer func() { _ = jr.Close() }()
	es, err := jr.Entries(0, 0)
	if err != nil {
		j.t.Fatal(err)
	}
	return es
}

// ---- the model

// codeAlphabet is the confirm code's alphabet, as the model holds it.
const codeAlphabet = "ACDEFGHJKLMNPQRTUVWXY34679"

// codeReader is the reader the model makes its confirm code from: it hands
// out the bytes of the codes the test wants, in order, the way the model
// reads them (16 bytes a time, the characters' places in the alphabet first
// and bytes the model refuses after).
type codeReader struct {
	codes []string
	pos   int
}

func (r *codeReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.codes) {
		return 0, io.ErrUnexpectedEOF
	}
	code := r.codes[r.pos]
	r.pos++
	for i := range p {
		p[i] = 0xff
	}
	for i := 0; i < len(code) && i < len(p); i++ {
		p[i] = byte(strings.IndexByte(codeAlphabet, code[i]))
	}
	return len(p), nil
}

// model drives the console's approvals model on a backend, with the test's
// clock, running each command it returns at once.
type model struct {
	t    *testing.T
	b    approvals.Backend
	m    *approvals.Model
	clk  *doortest.Clock
	w, h int
}

func newModel(t *testing.T, b approvals.Backend, clk *doortest.Clock, codes ...string) *model {
	t.Helper()
	m := approvals.New(approvals.Options{
		Backend: b, Now: clk.Now, Random: &codeReader{codes: codes},
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(clk.Now()) }
		},
	})
	md := &model{t: t, b: b, m: m, clk: clk, w: 100, h: 40}
	md.run(m.SetSize(md.w, md.h))
	md.run(m.Init())
	return md
}

func (md *model) run(cmd tea.Cmd) {
	md.t.Helper()
	if cmd == nil {
		return
	}
	switch v := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			md.run(c)
		}
	default:
		md.feed(v)
	}
}

func (md *model) feed(msg tea.Msg) approvals.Action {
	md.t.Helper()
	cmd, act := md.m.Update(msg)
	md.run(cmd)
	return act
}

// key presses a key. The console that holds the model tells the backend that
// the owner is active at each key press, before the model sees it; this does
// the same.
func (md *model) key(name string) approvals.Action {
	md.t.Helper()
	md.b.Activity(md.clk.Now())
	switch name {
	case "enter":
		return md.feed(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "down":
		return md.feed(tea.KeyPressMsg{Code: tea.KeyDown})
	case "space":
		return md.feed(tea.KeyPressMsg{Code: ' ', Text: " "})
	}
	rs := []rune(name)
	if len(rs) != 1 {
		md.t.Fatalf("no key %q", name)
	}
	return md.feed(tea.KeyPressMsg{Code: rs[0], Text: name})
}

// typ types the characters 100 ms apart, as a person does and a paste does
// not.
func (md *model) typ(s string) {
	md.t.Helper()
	for _, c := range s {
		md.clk.Advance(100 * time.Millisecond)
		md.key(string(c))
	}
}

func (md *model) screen() string {
	buf := uv.NewBuffer(md.w, md.h)
	md.m.Draw(buf, buf.Bounds())
	var rows []string
	for y := 0; y < md.h; y++ {
		var b strings.Builder
		for x := 0; x < md.w; x++ {
			if c := buf.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(rows, "\n")
}

// code reads the confirm code from the title row, or "".
func (md *model) code() string {
	title, _, _ := strings.Cut(md.screen(), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(title), "APPROVAL ")
	if !ok {
		return ""
	}
	if f := strings.Fields(rest); len(f) > 0 && len(f[0]) == 4 && strings.Trim(f[0], codeAlphabet) == "" {
		return f[0]
	}
	return ""
}

// readAndSign opens the first card of the list, reads it through by moving
// the cursor down it, and presses space on every row on the way: a row that
// is not a flagged path takes no sign-off, and a flagged path is passed once.
func (md *model) readAndSign() {
	md.t.Helper()
	md.key("enter")
	md.clk.Advance(time.Second)
	for i := 0; i < 400; i++ {
		md.key("down")
		md.key("space")
	}
}

// ---- helpers for the git side

// intakeMoves counts the intake's records of a moved target: one for each ref
// update the adapter made. The daemon is running, and the adapter reads its
// own records folder.
func (j *joined) intakeMoves() int {
	j.t.Helper()
	moves, err := j.o.Host.store.Intakes()
	if err != nil {
		j.t.Fatal(err)
	}
	return len(moves)
}

// mergeOf returns the merge commit of a card.
func mergeOf(p record.Pending) string { return p.Binding.Merge.Commit }

// pins returns the refs under the pending prefix.
func (j *joined) pins() []string {
	var out []string
	for _, line := range strings.Split(j.lab.refs(), "\n") {
		if name, _, _ := strings.Cut(line, " "); strings.HasPrefix(name, "refs/ataniq/pending/") {
			out = append(out, name)
		}
	}
	return out
}

// pendingOf reads a card's pending record at the door.
func (j *joined) pendingOf(id string) record.Pending { return j.pending(id) }

// askFor drops a request for the feature, with change applied to the request,
// and collects it: the host builds the merge, verifies it and plans it. It
// returns the request id and the card's id, which is empty when the request
// was refused.
func (j *joined) askFor(feature string, change func(*record.Request)) (reqID, cardID string) {
	j.t.Helper()
	id, b := j.request(feature, change)
	j.dropFile(id, b)
	if n, err := j.o.Collector.Scan(context.Background()); err != nil || n != 1 {
		j.t.Fatalf("Scan took %d requests: %v", n, err)
	}
	st, ok := j.o.Service.Status(id)
	if !ok {
		j.t.Fatalf("no status for %s", id)
	}
	return id, st.PendingID
}

// card2 asks for feature x and returns the request id and the card's id; the
// request must make a card.
func (j *joined) card2() (reqID, cardID string) {
	j.t.Helper()
	id, cid := j.askFor("x", nil)
	if cid == "" {
		st, _ := j.o.Service.Status(id)
		j.t.Fatalf("the request made no card: %+v", st)
	}
	return id, cid
}

// tap is a proxy between the console's client and the daemon that keeps every
// byte that passes in either direction.
type tap struct {
	mu   sync.Mutex
	up   bytes.Buffer // client to daemon
	down bytes.Buffer // daemon to client
	dir  string
	l    net.Listener
}

// startTap listens on a folder of its own and forwards to the daemon's
// socket. Point a console at its folder.
func (j *joined) startTap() *tap {
	j.t.Helper()
	dir := filepath.Join(j.base, "tap")
	l, _, err := clientapi.Listen(dir)
	if err != nil {
		j.t.Fatal(err)
	}
	tp := &tap{dir: dir, l: l}
	target := clientapi.SocketPath(j.runtime)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				d, err := net.Dial("unix", target)
				if err != nil {
					_ = c.Close()
					return
				}
				done := make(chan struct{}, 2)
				cp := func(dst, src net.Conn, keep *bytes.Buffer) {
					buf := make([]byte, 4096)
					for {
						n, err := src.Read(buf)
						if n > 0 {
							tp.mu.Lock()
							keep.Write(buf[:n])
							tp.mu.Unlock()
							if _, werr := dst.Write(buf[:n]); werr != nil {
								break
							}
						}
						if err != nil {
							break
						}
					}
					done <- struct{}{}
				}
				go cp(d, c, &tp.up)
				go cp(c, d, &tp.down)
				<-done
				_ = c.Close()
				_ = d.Close()
			}()
		}
	}()
	j.t.Cleanup(func() { _ = l.Close() })
	return tp
}

// bytesField matches the records that travel as base64 in the wire's results.
var bytesField = regexp.MustCompile(`"bytes":"([A-Za-z0-9+/=]*)"`)

// unwrapped returns the stream and, after it, every record that travelled in
// it as base64, decoded, so that a search sees the records' text.
func unwrapped(stream string) string {
	var b strings.Builder
	b.WriteString(stream)
	for _, m := range bytesField.FindAllStringSubmatch(stream, -1) {
		if raw, err := base64.StdEncoding.DecodeString(m[1]); err == nil {
			b.WriteString("\n")
			b.Write(raw)
		}
	}
	return b.String()
}

// decideParams returns the parameters of every owner.decide request in what
// the client sent.
func decideParams(t *testing.T, up string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(up, "\n") {
		if line == "" {
			continue
		}
		var req struct {
			Op     string                     `json:"op"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("the client sent a line that is not a request: %v", err)
		}
		if req.Op == ownerwire.OpDecide {
			out = append(out, req.Params)
		}
	}
	return out
}

// seen returns everything that passed, both ways.
func (tp *tap) seen() (up, down string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.up.String(), tp.down.String()
}

// tapped returns a console client that talks through the tap.
func (j *joined) tapped(tp *tap) *consoleclient.Owner {
	j.t.Helper()
	o, err := consoleclient.NewOwner(consoleclient.OwnerConfig{Dir: tp.dir, Client: e2eClient, Timeout: 30 * time.Second})
	if err != nil {
		j.t.Fatal(err)
	}
	j.t.Cleanup(func() { _ = o.Close() })
	return o
}

// readableFiles returns every regular file under the folders, with its bytes,
// by path, for a search. A folder named objects (the mirror's) is not read: git
// compresses it, so a search of it proves nothing.
func readableFiles(t *testing.T, roots ...string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "objects" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = b
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// holders returns the names, sorted, of the files that hold the text.
func holders(files map[string][]byte, text string) []string {
	var out []string
	for name, b := range files {
		if bytes.Contains(b, []byte(text)) {
			out = append(out, name)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for k := i; k > 0 && s[k-1] > s[k]; k-- {
			s[k-1], s[k] = s[k], s[k-1]
		}
	}
}
