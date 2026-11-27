package consoleclient

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadwire"
)

// The tests of the lead control client talk to the client protocol's own
// server on a private socket in a temporary folder, with the six operations
// registered through the extension seam and answered by a fake daemon that
// keeps a command table the way the design says: the same client and id with
// the same payload returns the stored receipt and runs nothing. A few need a
// peer that no real server can be; those use rawPeer. Nothing here starts a
// unit, a namespace or an agent.

const testRole = "scratch-alpha/lead"

// cmdParams is every member of the params of the five mutating operations,
// as the fake daemon reads them.
type cmdParams struct {
	Client     string `json:"client"`
	CommandID  string `json:"command_id"`
	Role       string `json:"role"`
	Epoch      uint64 `json:"epoch"`
	AttachRole string `json:"attach_role"`
	MessageID  string `json:"message_id"`
	Body       string `json:"body"`
}

func (p cmdParams) digest(op string) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s|%s", op, p.Role, p.Epoch, p.AttachRole, p.MessageID, p.Body)
}

// execFunc is what the fake daemon does with a command that is new.
type execFunc func(op string, p cmdParams) (leadwire.Status, string, any)

// leadDaemon is a fake daemon with lead control: a real server whose lead
// operations are answered from a command table.
type leadDaemon struct {
	*ownerFake

	tmu    sync.Mutex
	table  map[string]stored
	runs   map[string]int
	exec   execFunc
	hold   func(op string, p cmdParams)
	fail   map[string]*clientapi.ErrorBody
	list   *leadwire.ListResult
	rawRes map[string]json.RawMessage
}

type stored struct {
	digest string
	rc     leadwire.Receipt
}

var testTime = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// defaultExec finishes every command done, with a result of its operation.
func defaultExec(op string, p cmdParams) (leadwire.Status, string, any) {
	switch op {
	case leadwire.OpDefine:
		return leadwire.StatusDone, "", leadwire.DefineResult{Role: p.Role, State: "stopped", ProfileDigest: "sha256:abc"}
	case leadwire.OpStart:
		return leadwire.StatusDone, "", leadwire.StartResult{Role: p.Role, State: "starting", Epoch: 3, Unit: "ataniq-lead-scratch-alpha.service", Waiver: "w"}
	case leadwire.OpStop:
		return leadwire.StatusDone, "", leadwire.StopResult{Role: p.Role, State: "stopped", Proof: "empty"}
	case leadwire.OpTicket:
		e := p.Epoch
		if e == 0 {
			e = 3
		}
		return leadwire.StatusDone, "", leadwire.TicketResult{Role: p.Role, Epoch: e, TicketFile: "/srv/run/leads/alpha/ticket", Folder: "/srv/run/leads/alpha"}
	case leadwire.OpInject:
		return leadwire.StatusDone, "", leadwire.InjectResult{Role: p.Role, Epoch: p.Epoch, MessageID: p.MessageID,
			Result: leadwire.Delivery{Outcome: "submitted", Written: "all"}}
	}
	return leadwire.StatusRefused, "unknown-op", nil
}

func inProgress(string, cmdParams) (leadwire.Status, string, any) {
	return leadwire.StatusInProgress, "", nil
}

func startLeadDaemon(t *testing.T) *leadDaemon {
	t.Helper()
	d := &leadDaemon{
		ownerFake: startOwnerWith(t, clientapi.Options{}, leadwire.Operations()),
		table:     map[string]stored{},
		runs:      map[string]int{},
		exec:      defaultExec,
		fail:      map[string]*clientapi.ErrorBody{},
		rawRes:    map[string]json.RawMessage{},
	}
	for _, op := range leadwire.Operations() {
		op := op
		if op == leadwire.OpList {
			d.on(op, func(json.RawMessage) (any, *clientapi.ErrorBody) {
				d.tmu.Lock()
				defer d.tmu.Unlock()
				if e := d.fail[op]; e != nil {
					return nil, e
				}
				if d.list == nil {
					return leadwire.ListResult{MaxRunning: 2, Leads: []leadwire.Lead{}}, nil
				}
				return *d.list, nil
			})
			continue
		}
		d.on(op, func(params json.RawMessage) (any, *clientapi.ErrorBody) { return d.handle(op, params) })
	}
	return d
}

func (d *leadDaemon) handle(op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	var p cmdParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &clientapi.ErrorBody{Code: "invalid-request", Message: "bad params"}
	}
	d.tmu.Lock()
	if e := d.fail[op]; e != nil {
		d.tmu.Unlock()
		return nil, e
	}
	key := p.Client + "\x00" + p.CommandID
	if s, ok := d.table[key]; ok {
		d.tmu.Unlock()
		if s.digest != p.digest(op) {
			return nil, &clientapi.ErrorBody{Code: "conflict", Message: "the command id was used for another command"}
		}
		return s.rc, nil
	}
	rc := leadwire.Receipt{CommandID: p.CommandID, Op: op, Role: p.Role, Status: leadwire.StatusInProgress, AdmittedAt: testTime}
	d.table[key] = stored{digest: p.digest(op), rc: rc}
	hold, exec := d.hold, d.exec
	d.tmu.Unlock()

	if hold != nil {
		hold(op, p)
	}
	st, code, result := exec(op, p)
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.runs[op]++
	rc.Status, rc.Code = st, code
	if st.Final() {
		at := testTime.Add(time.Second)
		rc.FinishedAt = &at
	}
	if st == leadwire.StatusRefused || st == leadwire.StatusUnavailable {
		rc.Message = "refused: " + hostileMark + "\x1b[2J" + strings.Repeat("x", 100)
	}
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			panic(err)
		}
		rc.Result = b
	}
	d.table[key] = stored{digest: p.digest(op), rc: rc}
	return rc, nil
}

func (d *leadDaemon) setExec(f execFunc) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.exec = f
}

func (d *leadDaemon) setFail(op string, code clientapi.ErrorCode, msg string) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.fail[op] = &clientapi.ErrorBody{Code: code, Message: msg}
}

func (d *leadDaemon) clearFail(op string) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	delete(d.fail, op)
}

func (d *leadDaemon) ran(op string) int {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	return d.runs[op]
}

// ids returns the command ids of the requests of op that reached the daemon,
// in order.
func (d *leadDaemon) ids(op string) []string {
	var out []string
	for _, c := range d.of(op) {
		var p cmdParams
		if err := json.Unmarshal([]byte(c.params), &p); err != nil {
			d.t.Fatal(err)
		}
		out = append(out, p.CommandID)
	}
	return out
}

func (d *leadDaemon) setList(l leadwire.ListResult) {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	d.list = &l
}

// blocker makes a hold that blocks every command of op until release is
// called, and a release that is safe to call twice and runs at cleanup.
func blocker(t *testing.T, only string) (hold func(string, cmdParams), release func()) {
	ch := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return func(op string, _ cmdParams) {
		if op == only {
			<-ch
		}
	}, release
}

func newLeads(t *testing.T, dir string) *Leads {
	t.Helper()
	return newLeadsWith(t, dir, 5*time.Second)
}

func newLeadsWith(t *testing.T, dir string, timeout time.Duration) *Leads {
	t.Helper()
	l, err := NewLeads(LeadsConfig{Dir: dir, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func (l *Leads) pendingCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func leadHello() clientapi.HelloResult {
	return clientapi.HelloResult{
		Protocols:  clientapi.SupportedProtocols(),
		Operations: append(clientapi.Operations(), leadwire.Operations()...),
		Mutations:  true,
		Version:    "0.0.0-test",
	}
}

func isUnavailable(err error, state State) bool {
	var u *Unavailable
	return errors.As(err, &u) && u.State == state
}

// ---- detection ---------------------------------------------------------

func TestLeadsDetectionByHello(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		f := startOwnerWith(t, clientapi.Options{}, nil)
		l := newLeads(t, f.dir)
		if err := l.Detect(); !errors.Is(err, ErrNoLeadControl) {
			t.Fatalf("Detect: %v", err)
		}
		if _, err := l.List(); !errors.Is(err, ErrNoLeadControl) {
			t.Fatalf("List: %v", err)
		}
		if _, err := l.Start(testRole); !errors.Is(err, ErrNoLeadControl) {
			t.Fatalf("Start: %v", err)
		}
		if snap := f.counters.Snapshot(); len(f.sent()) != 0 || snap.Served[clientapi.OpHello] == 0 {
			t.Fatalf("operations reached the daemon: %v %+v", f.sent(), snap)
		}
		if l.conn != nil || l.pendingCount() != 0 {
			t.Fatal("something was kept for a daemon with no lead control")
		}
		if ErrNoLeadControl.Error() != "no lead control in this daemon" {
			t.Fatalf("text = %q", ErrNoLeadControl.Error())
		}
	})
	t.Run("the owner channel is not lead control", func(t *testing.T) {
		f := startOwnerWith(t, clientapi.Options{}, []string{"owner.decider", "owner.list"})
		l := newLeads(t, f.dir)
		if err := l.Detect(); !errors.Is(err, ErrNoLeadControl) {
			t.Fatalf("Detect: %v", err)
		}
	})
	for i, missing := range leadwire.Operations() {
		t.Run("without "+missing, func(t *testing.T) {
			all := leadwire.Operations()
			ops := append(all[:i], all[i+1:]...)
			f := startOwnerWith(t, clientapi.Options{}, ops)
			f.onResult(leadwire.OpList, []byte(`{"leads":[]}`))
			l := newLeads(t, f.dir)
			err := l.Detect()
			if len(f.sent()) != 0 {
				t.Fatalf("Detect sent %v", f.sent())
			}
			_, listErr := l.List()
			if missing == leadwire.OpList {
				if !errors.Is(err, ErrLeadsVersion) || !errors.Is(listErr, ErrLeadsVersion) || len(f.sent()) != 0 {
					t.Fatalf("Detect = %v, List = %v, sent %v", err, listErr, f.sent())
				}
			} else if err != nil || listErr != nil || f.count(leadwire.OpList) != 1 || len(f.sent()) != 1 {
				t.Fatalf("Detect = %v, List = %v, sent %v", err, listErr, f.sent())
			}
		})
	}
	t.Run("all six", func(t *testing.T) {
		d := startLeadDaemon(t)
		l := newLeads(t, d.dir)
		if err := l.Detect(); err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if len(d.sent()) != 0 {
			t.Fatalf("Detect sent %v", d.sent())
		}
		if l.conn == nil {
			t.Fatal("the connection was not kept")
		}
	})
	// The desk's two chat operations, which a scratch daemon lists after the
	// six, change nothing of the detection: beside the six they are lead
	// control, and alone they are none of it.
	t.Run("the chat operations beside the six", func(t *testing.T) {
		f := startOwnerWith(t, clientapi.Options{}, append(leadwire.Operations(), leadwire.ChatOperations()...))
		l := newLeads(t, f.dir)
		if err := l.Detect(); err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if len(f.sent()) != 0 {
			t.Fatalf("Detect sent %v", f.sent())
		}
	})
	t.Run("the chat operations alone", func(t *testing.T) {
		f := startOwnerWith(t, clientapi.Options{}, leadwire.ChatOperations())
		l := newLeads(t, f.dir)
		if err := l.Detect(); !errors.Is(err, ErrNoLeadControl) {
			t.Fatalf("Detect: %v", err)
		}
	})
	t.Run("another protocol", func(t *testing.T) {
		hello := leadHello()
		hello.Protocols = []string{"other.protocol/9"}
		p := startRawPeer(t, func(req clientapi.Request) []byte { return rawOK(t, req, hello) })
		l := newLeads(t, p.dir)
		if err := l.Detect(); !isUnavailable(err, StateOtherProtocol) {
			t.Fatalf("Detect: %v", err)
		}
		if ops := p.sent(); len(ops) != 1 || ops[0] != clientapi.OpHello {
			t.Fatalf("sent %v", ops)
		}
	})
	t.Run("no daemon", func(t *testing.T) {
		l := newLeads(t, socketDir(t))
		if err := l.Detect(); !isUnavailable(err, StateNoDaemon) {
			t.Fatalf("Detect: %v", err)
		}
	})
}

// ---- construction ------------------------------------------------------

func TestNewLeadsClientString(t *testing.T) {
	a, err := NewLeads(LeadsConfig{Dir: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewLeads(LeadsConfig{Dir: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.client == b.client {
		t.Fatalf("two runs share the client string %q", a.client)
	}
	for _, l := range []*Leads{a, b} {
		if !strings.HasPrefix(l.client, "ataniq-console-") || len(l.client) != len("ataniq-console-")+16 {
			t.Fatalf("client = %q", l.client)
		}
		for i := 0; i < len(l.client); i++ {
			if l.client[i] < 0x21 || l.client[i] > 0x7e {
				t.Fatalf("client %q has an unplain byte", l.client)
			}
		}
	}
	long, err := NewLeads(LeadsConfig{Dir: "x", Client: strings.Repeat("n", maxLeadsName)})
	if err != nil || len(long.client) != leadwire.MaxClient {
		t.Fatalf("the longest name: %v %v", err, long)
	}
	for _, name := range []string{strings.Repeat("n", maxLeadsName+1), "has space", "tab\t", "café"} {
		if _, err := NewLeads(LeadsConfig{Dir: "x", Client: name}); err == nil {
			t.Errorf("the name %q was taken", name)
		}
	}
	if _, err := NewLeads(LeadsConfig{Dir: "x", Random: bytes.NewReader([]byte{1, 2})}); err == nil {
		t.Error("a session id was made from too few bytes")
	}
}

func TestCommandIdsAre128RandomBits(t *testing.T) {
	var src bytes.Buffer
	src.Write([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	src.Write(bytes.Repeat([]byte{0xab}, 16))
	src.Write(bytes.Repeat([]byte{0xcd}, 16))
	d := startLeadDaemon(t)
	l, err := NewLeads(LeadsConfig{Dir: d.dir, Random: &src, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.client != "ataniq-console-0001020304050607" {
		t.Fatalf("client = %q", l.client)
	}
	if _, err := l.Start(testRole); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Define(testRole); err != nil {
		t.Fatal(err)
	}
	if got := d.ids(leadwire.OpStart); len(got) != 1 || got[0] != strings.Repeat("ab", 16) {
		t.Fatalf("start id = %v", got)
	}
	if got := d.ids(leadwire.OpDefine); len(got) != 1 || got[0] != strings.Repeat("cd", 16) {
		t.Fatalf("define id = %v", got)
	}
	// The source has run out: no id can be made, and nothing is sent.
	before := len(d.sent())
	if _, err := l.Stop(testRole, 1); !errors.Is(err, ErrNoCommandID) {
		t.Fatalf("Stop: %v", err)
	}
	if len(d.sent()) != before || l.pendingCount() != 0 {
		t.Fatal("a command was sent or kept without an id")
	}
}

// ---- the wire ----------------------------------------------------------

func TestEveryOperationGoesOutAsLeadwireWritesIt(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	hexID := func(s string) bool {
		b, err := hex.DecodeString(s)
		return err == nil && len(b) == 16 && s == strings.ToLower(s)
	}
	check := func(op, want string) {
		t.Helper()
		calls := d.of(op)
		if len(calls) != 1 {
			t.Fatalf("%s: %d requests", op, len(calls))
		}
		var p cmdParams
		if err := json.Unmarshal([]byte(calls[0].params), &p); err != nil {
			t.Fatal(err)
		}
		if p.Client != l.client || !hexID(p.CommandID) {
			t.Fatalf("%s: client %q, id %q", op, p.Client, p.CommandID)
		}
		got := strings.Replace(calls[0].params, p.Client, "C", 1)
		got = strings.Replace(got, p.CommandID, "I", 1)
		if got != want {
			t.Fatalf("%s: params\n got %s\nwant %s", op, got, want)
		}
	}
	if _, err := l.Define(testRole); err != nil {
		t.Fatal(err)
	}
	check(leadwire.OpDefine, `{"client":"C","command_id":"I","role":"scratch-alpha/lead"}`)
	if _, err := l.Start(testRole); err != nil {
		t.Fatal(err)
	}
	check(leadwire.OpStart, `{"client":"C","command_id":"I","role":"scratch-alpha/lead"}`)
	if _, err := l.Stop(testRole, 0); err != nil {
		t.Fatal(err)
	}
	check(leadwire.OpStop, `{"client":"C","command_id":"I","role":"scratch-alpha/lead","epoch":0}`)
	if _, err := l.Ticket(testRole, 3, true); err != nil {
		t.Fatal(err)
	}
	check(leadwire.OpTicket, `{"client":"C","command_id":"I","role":"scratch-alpha/lead","epoch":3,"attach_role":"writer"}`)
	if _, err := l.Send(testRole, 3, "m1", "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	check(leadwire.OpInject, `{"client":"C","command_id":"I","role":"scratch-alpha/lead","epoch":3,"message_id":"m1","body":"hello\nworld"}`)
	if _, err := l.List(); err != nil {
		t.Fatal(err)
	}
	if calls := d.of(leadwire.OpList); len(calls) != 1 || calls[0].params != "" {
		t.Fatalf("lead.list went out with params: %+v", calls)
	}
	// A viewer ticket.
	if _, err := l.Ticket(testRole, 3, false); err != nil {
		t.Fatal(err)
	}
	if got := d.of(leadwire.OpTicket)[1].params; !strings.Contains(got, `"attach_role":"viewer"`) {
		t.Fatalf("viewer ticket: %s", got)
	}
}

func TestReceiptsCarryTheirFields(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	r, err := l.Define(testRole)
	if err != nil || r.Op != LeadOpDefine || r.Role != testRole || r.Status != LeadDone || r.CommandID == "" {
		t.Fatalf("define: %+v %v", r, err)
	}
	r, err = l.Start(testRole)
	if err != nil || r.Op != LeadOpStart || r.Status != LeadDone || r.Epoch != 3 {
		t.Fatalf("start: %+v %v", r, err)
	}
	r, err = l.Stop(testRole, 3)
	if err != nil || r.Op != LeadOpStop || r.Status != LeadDone {
		t.Fatalf("stop: %+v %v", r, err)
	}
	r, err = l.Ticket(testRole, 3, true)
	if err != nil || r.Op != LeadOpTicket || r.Status != LeadDone || r.Epoch != 3 ||
		r.Folder != "/srv/run/leads/alpha" || r.TicketFile != "/srv/run/leads/alpha/ticket" {
		t.Fatalf("ticket: %+v %v", r, err)
	}
	r, err = l.Ticket(testRole, 0, false)
	if err != nil || r.Epoch != 3 {
		t.Fatalf("ticket of the current epoch: %+v %v", r, err)
	}
	r, err = l.Send(testRole, 3, "m1", "hi")
	if err != nil || r.Op != LeadOpInject || r.Status != LeadDone || r.Epoch != 3 || r.Outcome != "submitted" {
		t.Fatalf("inject: %+v %v", r, err)
	}
	if l.pendingCount() != 0 {
		t.Fatalf("%d commands are kept after final receipts", l.pendingCount())
	}
}

// ---- the replay discipline ---------------------------------------------

func TestACommandWithNoFinalReceiptRepeatsItsId(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	first, err := l.Start(testRole)
	if err != nil || first.Status != LeadInProgress {
		t.Fatalf("first: %+v %v", first, err)
	}
	if l.pendingCount() != 1 {
		t.Fatalf("pending = %d", l.pendingCount())
	}
	for i := 0; i < 3; i++ {
		again, err := l.Start(testRole)
		if err != nil || again.CommandID != first.CommandID || again.Status != LeadInProgress {
			t.Fatalf("repeat %d: %+v %v", i, again, err)
		}
	}
	ids := d.ids(leadwire.OpStart)
	if len(ids) != 4 {
		t.Fatalf("%d requests", len(ids))
	}
	for _, id := range ids {
		if id != first.CommandID {
			t.Fatalf("ids = %v", ids)
		}
	}
	// The daemon finishes the command: the next call gets its final receipt
	// under the same id, and the call after that is a new command.
	d.setExec(defaultExec)
	d.tmu.Lock()
	for k, s := range d.table {
		rc := s.rc
		rc.Status = leadwire.StatusDone
		rc.Result = json.RawMessage(`{"role":"` + testRole + `","state":"starting","epoch":4,"unit":"u","waiver":"w"}`)
		d.table[k] = stored{digest: s.digest, rc: rc}
	}
	d.tmu.Unlock()
	done, err := l.Start(testRole)
	if err != nil || done.Status != LeadDone || done.CommandID != first.CommandID || done.Epoch != 4 {
		t.Fatalf("final: %+v %v", done, err)
	}
	if l.pendingCount() != 0 {
		t.Fatal("the id was kept after the final receipt")
	}
	next, err := l.Start(testRole)
	if err != nil || next.CommandID == first.CommandID {
		t.Fatalf("next: %+v %v", next, err)
	}
	// Two commands were carried out: the first, and the next.
	if d.ran(leadwire.OpStart) != 2 {
		t.Fatalf("ran %d starts", d.ran(leadwire.OpStart))
	}
}

func TestDifferentArgumentsAreDifferentCommands(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	ids := map[string]string{}
	add := func(name string, r LeadReceipt, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for n, id := range ids {
			if id == r.CommandID {
				t.Fatalf("%s and %s share the id %s", name, n, id)
			}
		}
		ids[name] = r.CommandID
	}
	r, err := l.Start(testRole)
	add("start", r, err)
	r, err = l.Define(testRole)
	add("define", r, err)
	r, err = l.Start("scratch-beta/lead")
	add("start beta", r, err)
	r, err = l.Stop(testRole, 1)
	add("stop 1", r, err)
	r, err = l.Stop(testRole, 2)
	add("stop 2", r, err)
	r, err = l.Ticket(testRole, 1, true)
	add("ticket writer", r, err)
	r, err = l.Ticket(testRole, 1, false)
	add("ticket viewer", r, err)
	r, err = l.Send(testRole, 1, "m1", "a")
	add("send a", r, err)
	r, err = l.Send(testRole, 1, "m1", "b")
	add("send b", r, err)
	r, err = l.Send(testRole, 1, "m2", "a")
	add("send m2", r, err)
	r, err = l.Send(testRole, 2, "m1", "a")
	add("send epoch 2", r, err)
	// Each of them repeats its own id.
	r, err = l.Stop(testRole, 2)
	if err != nil || r.CommandID != ids["stop 2"] {
		t.Fatalf("stop 2 again: %+v %v", r, err)
	}
	r, err = l.Send(testRole, 1, "m1", "b")
	if err != nil || r.CommandID != ids["send b"] {
		t.Fatalf("send b again: %+v %v", r, err)
	}
	d.tmu.Lock()
	n := len(d.table)
	d.tmu.Unlock()
	if n != len(ids) {
		t.Fatalf("the daemon holds %d commands for %d", n, len(ids))
	}
}

func TestTwoCallsAtOnceUseOneId(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	var wg sync.WaitGroup
	got := make([]LeadReceipt, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = l.Start(testRole)
		}()
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i].CommandID != got[0].CommandID {
			t.Fatalf("call %d: %+v %v", i, got[i], errs[i])
		}
	}
	d.tmu.Lock()
	n := len(d.table)
	d.tmu.Unlock()
	if n != 1 {
		t.Fatalf("the daemon holds %d commands", n)
	}
}

func TestAListThatShowsTheCommandFinalForgetsIt(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	r, err := l.Start(testRole)
	if err != nil {
		t.Fatal(err)
	}
	row := func(last *leadwire.Receipt) leadwire.ListResult {
		return leadwire.ListResult{MaxRunning: 2, Leads: []leadwire.Lead{{
			Key: "alpha", Role: testRole, Provision: "done", State: "ready", Last: last,
		}}}
	}
	// In progress, or another command's receipt: nothing is forgotten.
	d.setList(row(&leadwire.Receipt{CommandID: r.CommandID, Op: leadwire.OpStart, Role: testRole, Status: leadwire.StatusInProgress, AdmittedAt: testTime}))
	if _, err := l.List(); err != nil || l.pendingCount() != 1 {
		t.Fatalf("in progress: %v %d", err, l.pendingCount())
	}
	d.setList(row(&leadwire.Receipt{CommandID: "other", Op: leadwire.OpStart, Role: testRole, Status: leadwire.StatusDone, AdmittedAt: testTime}))
	if _, err := l.List(); err != nil || l.pendingCount() != 1 {
		t.Fatalf("another id: %v %d", err, l.pendingCount())
	}
	d.setList(row(&leadwire.Receipt{CommandID: r.CommandID, Op: leadwire.OpStart, Role: testRole, Status: leadwire.StatusUncertain, AdmittedAt: testTime}))
	lst, err := l.List()
	if err != nil || l.pendingCount() != 0 {
		t.Fatalf("uncertain: %v %d", err, l.pendingCount())
	}
	if len(lst.Leads) != 1 || lst.Leads[0].Last == nil || lst.Leads[0].Last.Status != LeadUncertain || lst.Leads[0].Last.Op != LeadOpStart {
		t.Fatalf("listing = %+v", lst)
	}
	next, err := l.Start(testRole)
	if err != nil || next.CommandID == r.CommandID {
		t.Fatalf("next: %+v %v", next, err)
	}
}

func TestAnUncertainReceiptIsFinalAndNothingIsResent(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(func(string, cmdParams) (leadwire.Status, string, any) { return leadwire.StatusUncertain, "", nil })
	l := newLeads(t, d.dir)
	r, err := l.Start(testRole)
	if err != nil || r.Status != LeadUncertain {
		t.Fatalf("%+v %v", r, err)
	}
	if l.pendingCount() != 0 {
		t.Fatal("the id of an uncertain command was kept")
	}
	if d.count(leadwire.OpStart) != 1 {
		t.Fatalf("%d requests", d.count(leadwire.OpStart))
	}
}

func TestRefusedAndUnavailableReceiptsCarryOnlyAGrammarWord(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	for _, tc := range []struct {
		st   leadwire.Status
		code string
		want string
	}{
		{leadwire.StatusRefused, "max-running", "max-running"},
		{leadwire.StatusRefused, "second-message", "second-message"},
		{leadwire.StatusRefused, "profile-not-launchable", "profile-not-launchable"},
		{leadwire.StatusRefused, "holder-version-skew", "holder-version-skew"},
		{leadwire.StatusUnavailable, "holder-down", "holder-down"},
		{leadwire.StatusRefused, "", ""},
		{leadwire.StatusRefused, "Max Running!", "unrecognised"},
		{leadwire.StatusRefused, "‮EVIL", "unrecognised"},
		{leadwire.StatusRefused, "9lives", "unrecognised"},
		{leadwire.StatusRefused, strings.Repeat("a", 65), "unrecognised"},
	} {
		st, code := tc.st, tc.code
		d.setExec(func(string, cmdParams) (leadwire.Status, string, any) { return st, code, nil })
		r, err := l.Start(testRole)
		if err != nil || r.Status != LeadStatus(tc.st) || r.Code != tc.want {
			t.Fatalf("%v %q: %+v %v", tc.st, tc.code, r, err)
		}
		// The daemon's message (hostile in this fake) is nowhere.
		if s := fmt.Sprintf("%+v %#v", r, r); strings.Contains(s, "EVIL") || strings.Contains(s, "refused: ") || strings.Contains(s, "\x1b") {
			t.Fatalf("daemon text in %s", s)
		}
	}
}

func TestInjectOutcomeIsAGrammarWord(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	for outcome, want := range map[string]string{
		"submitted": "submitted", "not-now": "not-now", "refused": "refused", "uncertain": "uncertain",
		"Weird Outcome": "unrecognised", "": "",
	} {
		outcome := outcome
		d.setExec(func(op string, p cmdParams) (leadwire.Status, string, any) {
			return leadwire.StatusDone, "", leadwire.InjectResult{Role: p.Role, Epoch: p.Epoch, MessageID: p.MessageID,
				Result: leadwire.Delivery{Outcome: outcome, Written: "none", Detail: hostileMark}}
		})
		r, err := l.Send(testRole, 2, "m-"+strconv.Itoa(len(want)), "hi")
		if err != nil || r.Outcome != want {
			t.Fatalf("%q: %+v %v", outcome, r, err)
		}
		d.tmu.Lock()
		d.table = map[string]stored{}
		d.tmu.Unlock()
	}
}

// A done message carries the holder's reason and written words next to the
// outcome, each cleaned to a word of the grammar, and never its detail.
func TestInjectReasonAndWrittenAreGrammarWords(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	for i, tc := range []struct {
		res                      leadwire.Delivery
		outcome, reason, written string
	}{
		{leadwire.Delivery{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}, "not-now", "not-ready", "nothing"},
		{leadwire.Delivery{Outcome: "submitted", Written: "whole"}, "submitted", "", "whole"},
		{leadwire.Delivery{Outcome: "uncertain", Reason: "died", Written: "unknown"}, "uncertain", "died", "unknown"},
		{leadwire.Delivery{Outcome: "refused", Reason: "Weird Reason", Written: "\x1b[31m"}, "refused", "unrecognised", "unrecognised"},
		{leadwire.Delivery{Outcome: "refused", Reason: strings.Repeat("a", 65), Written: "nothing"}, "refused", "unrecognised", "nothing"},
	} {
		tc := tc
		tc.res.Detail = hostileMark
		d.setExec(func(op string, p cmdParams) (leadwire.Status, string, any) {
			return leadwire.StatusDone, "", leadwire.InjectResult{Role: p.Role, Epoch: p.Epoch, MessageID: p.MessageID, Result: tc.res}
		})
		r, err := l.Send(testRole, 2, "m-"+strconv.Itoa(i), "hi")
		if err != nil || r.Outcome != tc.outcome || r.Reason != tc.reason || r.Written != tc.written {
			t.Fatalf("%+v: got %+v %v", tc, r, err)
		}
		if s := fmt.Sprintf("%+v", r); strings.Contains(s, "EVIL") {
			t.Fatalf("the detail is in %s", s)
		}
	}
}

// ---- a lost answer -----------------------------------------------------

func TestALostAnswerIsAskedForWithTheSameIdOnANewConnection(t *testing.T) {
	d := startLeadDaemon(t)
	hold, release := blocker(t, leadwire.OpStart)
	d.hold = hold
	l := newLeadsWith(t, d.dir, 300*time.Millisecond)
	r, err := l.Start(testRole)
	// The first answer never came: the repeat found the command in progress.
	if err != nil || r.Status != LeadInProgress {
		t.Fatalf("Start: %+v %v", r, err)
	}
	ids := d.ids(leadwire.OpStart)
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != r.CommandID {
		t.Fatalf("ids = %v, receipt %s", ids, r.CommandID)
	}
	calls := d.of(leadwire.OpStart)
	if calls[0].params != calls[1].params {
		t.Fatalf("the repeat differs:\n%s\n%s", calls[0].params, calls[1].params)
	}
	if d.ran(leadwire.OpStart) != 0 {
		t.Fatal("the command ran before it was released")
	}
	if l.pendingCount() != 1 {
		t.Fatalf("pending = %d", l.pendingCount())
	}
	// It ends; asking again returns the final receipt under the same id,
	// and the command ran once.
	release()
	eventually(t, "the command to finish", func() bool { return d.ran(leadwire.OpStart) == 1 })
	done, err := l.Start(testRole)
	if err != nil || done.Status != LeadDone || done.CommandID != r.CommandID || done.Epoch != 3 {
		t.Fatalf("after: %+v %v", done, err)
	}
	if d.ran(leadwire.OpStart) != 1 || l.pendingCount() != 0 {
		t.Fatalf("ran %d, pending %d", d.ran(leadwire.OpStart), l.pendingCount())
	}
}

func TestALostAnswerTwiceReturnsAnErrorAndKeepsTheId(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeadsWith(t, d.dir, 200*time.Millisecond)
	// A daemon that takes the request and cannot answer: both the first send
	// and the repeat wait until the test releases them.
	gate := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(free)
	d.on(leadwire.OpStop, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		<-gate
		return nil, &clientapi.ErrorBody{Code: "internal", Message: "late"}
	})
	_, err := l.Stop(testRole, 2)
	if !errors.Is(err, ErrAnswerLost) {
		t.Fatalf("Stop: %v", err)
	}
	// Exactly one repeat: two requests, one id.
	ids := d.ids(leadwire.OpStop)
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("ids = %v", ids)
	}
	if l.pendingCount() != 1 {
		t.Fatalf("pending = %d", l.pendingCount())
	}
	// The daemon is back: the next call with the same arguments repeats the
	// same id.
	free()
	d.on(leadwire.OpStop, func(params json.RawMessage) (any, *clientapi.ErrorBody) { return d.handle(leadwire.OpStop, params) })
	r, err := l.Stop(testRole, 2)
	if err != nil || r.CommandID != ids[0] || r.Status != LeadDone {
		t.Fatalf("again: %+v %v (ids %v)", r, err, ids)
	}
	if l.pendingCount() != 0 {
		t.Fatal("the id was kept after the final receipt")
	}
}

// hangUpPeer is a peer that answers hello and hangs up on the first n
// requests of a lead operation, then answers them with a done receipt.
func hangUpPeer(t *testing.T, hangUps int) (*rawPeer, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var ids []string
	n := 0
	p := startRawPeer(t, func(req clientapi.Request) []byte {
		if req.Op == clientapi.OpHello {
			return rawOK(t, req, leadHello())
		}
		var cp cmdParams
		if err := json.Unmarshal(req.Params, &cp); err != nil {
			t.Error(err)
			return []byte{}
		}
		mu.Lock()
		ids = append(ids, cp.CommandID)
		n++
		hang := n <= hangUps
		mu.Unlock()
		if hang {
			return []byte{}
		}
		res, _ := json.Marshal(leadwire.StartResult{Role: cp.Role, State: "starting", Epoch: 5, Unit: "u", Waiver: "w"})
		return rawOK(t, req, leadwire.Receipt{CommandID: cp.CommandID, Op: req.Op, Role: cp.Role, Status: leadwire.StatusDone, Result: res, AdmittedAt: testTime})
	})
	return p, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), ids...) }
}

func TestAHangUpIsAskedForOnceOnANewConnection(t *testing.T) {
	p, ids := hangUpPeer(t, 1)
	l := newLeads(t, p.dir)
	r, err := l.Start(testRole)
	if err != nil || r.Status != LeadDone || r.Epoch != 5 {
		t.Fatalf("Start: %+v %v", r, err)
	}
	if got := p.sent(); strings.Join(got, ",") != "hello,lead.start,hello,lead.start" {
		t.Fatalf("sent %v", got)
	}
	if got := ids(); len(got) != 2 || got[0] != got[1] || got[0] != r.CommandID {
		t.Fatalf("ids = %v", got)
	}
}

func TestTwoHangUpsReturnAnErrorAndTheNextCallRepeatsTheId(t *testing.T) {
	p, ids := hangUpPeer(t, 2)
	l := newLeads(t, p.dir)
	if _, err := l.Start(testRole); !errors.Is(err, ErrAnswerLost) {
		t.Fatalf("Start: %v", err)
	}
	if got := strings.Join(p.sent(), ","); got != "hello,lead.start,hello,lead.start" {
		t.Fatalf("sent %v", got)
	}
	if l.pendingCount() != 1 || l.conn != nil {
		t.Fatalf("pending %d, conn %v", l.pendingCount(), l.conn)
	}
	r, err := l.Start(testRole)
	if err != nil || r.Status != LeadDone {
		t.Fatalf("again: %+v %v", r, err)
	}
	got := ids()
	if len(got) != 3 || got[0] != got[1] || got[1] != got[2] {
		t.Fatalf("ids = %v", got)
	}
}

func TestAStaleConnectionIsReplacedByTheRepeat(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	first, err := l.Start(testRole)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon restarts and keeps its command table, as the durable one
	// does. The client's connection is dead.
	d.restart()
	again, err := l.Start(testRole)
	if err != nil || again.CommandID != first.CommandID || again.Status != LeadInProgress {
		t.Fatalf("after the restart: %+v %v", again, err)
	}
	if lst, err := l.List(); err != nil || lst.MaxRunning != 2 {
		t.Fatalf("List after the restart: %+v %v", lst, err)
	}
	d.restart()
	if _, err := l.List(); err != nil {
		t.Fatalf("List on a stale connection: %v", err)
	}
	if d.count(leadwire.OpList) < 2 {
		t.Fatal("the list was not asked again on the new connection")
	}
}

// ---- errors before admission --------------------------------------------

func TestErrorsBeforeAdmission(t *testing.T) {
	hostile := "text \u202eEVIL " + strings.Repeat("x", 100)
	for _, tc := range []struct {
		code      clientapi.ErrorCode
		want      error
		wantState State
		keepsID   bool
	}{
		{"invalid-request", ErrLeadRejected, "", false},
		{"refused", ErrLeadNotListed, "", false},
		{"conflict", ErrCommandConflict, "", false},
		{"unavailable", ErrCommandUnavailable, "", true},
		{clientapi.CodeInternal, nil, StateUnavailable, true},
		{"some-new-code", nil, StateBadAnswer, true},
	} {
		d := startLeadDaemon(t)
		l := newLeads(t, d.dir)
		d.setFail(leadwire.OpStart, tc.code, hostile)
		_, err := l.Start(testRole)
		switch {
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Fatalf("%s: %v, want %v", tc.code, err, tc.want)
		case tc.want == nil && !isUnavailable(err, tc.wantState):
			t.Fatalf("%s: %v, want state %s", tc.code, err, tc.wantState)
		}
		if err == nil || strings.Contains(err.Error(), "EVIL") || strings.Contains(err.Error(), "text ") || strings.ContainsAny(err.Error(), "\x1b\n‮") {
			t.Fatalf("%s: the error has the daemon's text: %q", tc.code, err)
		}
		if (l.pendingCount() == 1) != tc.keepsID {
			t.Fatalf("%s: pending = %d", tc.code, l.pendingCount())
		}
		first := d.ids(leadwire.OpStart)[0]
		d.clearFail(leadwire.OpStart)
		r, err := l.Start(testRole)
		if err != nil || r.Status != LeadDone {
			t.Fatalf("%s: next: %+v %v", tc.code, r, err)
		}
		if (r.CommandID == first) != tc.keepsID {
			t.Fatalf("%s: the next call's id %s, first %s, keepsID %v", tc.code, r.CommandID, first, tc.keepsID)
		}
		l.Close()
		d.stop()
	}
}

func TestAnErrorThatEndsTheConnectionDropsItAndKeepsNothing(t *testing.T) {
	for _, code := range []clientapi.ErrorCode{clientapi.CodeBusy, clientapi.CodeUnauthorized} {
		code := code
		var mu sync.Mutex
		fail := true
		p := startRawPeer(t, func(req clientapi.Request) []byte {
			if req.Op == clientapi.OpHello {
				return rawOK(t, req, leadHello())
			}
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return rawErr(t, req, code, "no")
			}
			var cp cmdParams
			json.Unmarshal(req.Params, &cp)
			return rawOK(t, req, leadwire.Receipt{CommandID: cp.CommandID, Op: req.Op, Role: cp.Role, Status: leadwire.StatusRefused, Code: "x", AdmittedAt: testTime})
		})
		l := newLeads(t, p.dir)
		_, err := l.Start(testRole)
		want := StateUnavailable
		if code == clientapi.CodeUnauthorized {
			want = StateRefused
		}
		if !isUnavailable(err, want) {
			t.Fatalf("%s: %v", code, err)
		}
		if l.pendingCount() != 0 {
			t.Fatalf("%s: the id of a command that was not served was kept", code)
		}
		mu.Lock()
		fail = false
		mu.Unlock()
		if r, err := l.Start(testRole); err != nil || r.Status != LeadRefused {
			t.Fatalf("%s: next: %+v %v", code, r, err)
		}
	}
}

// ---- answers that are not valid -----------------------------------------

// scriptedPeer answers every lead operation with what reply returns for the
// params, and records the command ids it was sent.
func scriptedPeer(t *testing.T, reply func(req clientapi.Request, p cmdParams) any) (*rawPeer, func() []string, func() int) {
	t.Helper()
	var mu sync.Mutex
	var ids []string
	hellos := 0
	p := startRawPeer(t, func(req clientapi.Request) []byte {
		if req.Op == clientapi.OpHello {
			mu.Lock()
			hellos++
			mu.Unlock()
			return rawOK(t, req, leadHello())
		}
		var cp cmdParams
		json.Unmarshal(req.Params, &cp)
		mu.Lock()
		ids = append(ids, cp.CommandID)
		mu.Unlock()
		return rawOK(t, req, reply(req, cp))
	})
	return p, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), ids...) },
		func() int { mu.Lock(); defer mu.Unlock(); return hellos }
}

func receiptFor(req clientapi.Request, p cmdParams, st leadwire.Status, result any) leadwire.Receipt {
	r := leadwire.Receipt{CommandID: p.CommandID, Op: req.Op, Role: p.Role, Status: st, AdmittedAt: testTime}
	if result != nil {
		r.Result, _ = json.Marshal(result)
	}
	return r
}

func TestAnAnswerThatIsNotTheCommandsReceiptIsABadAnswer(t *testing.T) {
	type call func(l *Leads) error
	start := func(l *Leads) error { _, err := l.Start(testRole); return err }
	ticket := func(l *Leads) error { _, err := l.Ticket(testRole, 2, true); return err }
	send := func(l *Leads) error { _, err := l.Send(testRole, 2, "m1", "hi"); return err }
	define := func(l *Leads) error { _, err := l.Define(testRole); return err }
	stop := func(l *Leads) error { _, err := l.Stop(testRole, 2); return err }
	okTicket := leadwire.TicketResult{Role: testRole, Epoch: 2, TicketFile: "/srv/run/t", Folder: "/srv/run"}
	okInject := leadwire.InjectResult{Role: testRole, Epoch: 2, MessageID: "m1", Result: leadwire.Delivery{Outcome: "submitted", Written: "all"}}
	okStart := leadwire.StartResult{Role: testRole, State: "starting", Epoch: 2, Unit: "u", Waiver: "w"}
	cases := []struct {
		name string
		do   call
		mk   func(req clientapi.Request, p cmdParams) any
	}{
		{"another command id", start, func(req clientapi.Request, p cmdParams) any {
			r := receiptFor(req, p, leadwire.StatusDone, okStart)
			r.CommandID = "someone-elses"
			return r
		}},
		{"another operation", start, func(req clientapi.Request, p cmdParams) any {
			r := receiptFor(req, p, leadwire.StatusDone, okStart)
			r.Op = leadwire.OpStop
			return r
		}},
		{"another role", start, func(req clientapi.Request, p cmdParams) any {
			r := receiptFor(req, p, leadwire.StatusDone, okStart)
			r.Role = "scratch-beta/lead"
			return r
		}},
		{"an unknown status", start, func(req clientapi.Request, p cmdParams) any {
			return receiptFor(req, p, "maybe", nil)
		}},
		{"an empty status", start, func(req clientapi.Request, p cmdParams) any {
			return receiptFor(req, p, "", nil)
		}},
		{"an unknown member", start, func(req clientapi.Request, p cmdParams) any {
			b, _ := json.Marshal(receiptFor(req, p, leadwire.StatusRefused, nil))
			var m map[string]any
			json.Unmarshal(b, &m)
			m["extra"] = true
			return m
		}},
		{"a done start with no result", start, func(req clientapi.Request, p cmdParams) any {
			return receiptFor(req, p, leadwire.StatusDone, nil)
		}},
		{"a done start with another role's result", start, func(req clientapi.Request, p cmdParams) any {
			r := okStart
			r.Role = "scratch-beta/lead"
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"a done define with an unknown member", define, func(req clientapi.Request, p cmdParams) any {
			return receiptFor(req, p, leadwire.StatusDone, map[string]any{"role": testRole, "state": "s", "profile_digest": "d", "more": 1})
		}},
		{"a done stop of another role", stop, func(req clientapi.Request, p cmdParams) any {
			return receiptFor(req, p, leadwire.StatusDone, leadwire.StopResult{Role: "scratch-beta/lead", State: "s", Proof: "p"})
		}},
		{"a ticket for another epoch", ticket, func(req clientapi.Request, p cmdParams) any {
			r := okTicket
			r.Epoch = 3
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"a relative ticket path", ticket, func(req clientapi.Request, p cmdParams) any {
			r := okTicket
			r.TicketFile = "run/t"
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"an unclean folder", ticket, func(req clientapi.Request, p cmdParams) any {
			r := okTicket
			r.Folder = "/srv/run/../x"
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"an escape in a path", ticket, func(req clientapi.Request, p cmdParams) any {
			r := okTicket
			r.TicketFile = "/srv/run/\x1b[2Jt"
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"an empty folder", ticket, func(req clientapi.Request, p cmdParams) any {
			r := okTicket
			r.Folder = ""
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"an inject of another message", send, func(req clientapi.Request, p cmdParams) any {
			r := okInject
			r.MessageID = "m2"
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"an inject of another epoch", send, func(req clientapi.Request, p cmdParams) any {
			r := okInject
			r.Epoch = 9
			return receiptFor(req, p, leadwire.StatusDone, r)
		}},
		{"a result that is not an object", send, func(req clientapi.Request, p cmdParams) any {
			r := receiptFor(req, p, leadwire.StatusDone, nil)
			r.Result = json.RawMessage(`"x"`)
			return r
		}},
	}
	// The first six are not a receipt of this command, so the command has no
	// final receipt and its id is kept. The rest are a final receipt (done)
	// whose result is not usable: the command is over, the daemon will not run
	// it again, so the id is let go and the next call is a new command.
	const notAReceipt = 6
	for i, tc := range cases {
		p, ids, hellos := scriptedPeer(t, tc.mk)
		l := newLeads(t, p.dir)
		err := tc.do(l)
		if !isUnavailable(err, StateBadAnswer) {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if l.conn != nil {
			t.Errorf("%s: the connection was kept", tc.name)
		}
		wantKept := i < notAReceipt
		if (l.pendingCount() == 1) != wantKept {
			t.Errorf("%s: pending = %d", tc.name, l.pendingCount())
		}
		tc.do(l)
		got := ids()
		if len(got) != 2 || (got[0] == got[1]) != wantKept {
			t.Errorf("%s: ids = %v, kept %v", tc.name, got, wantKept)
		}
		if hellos() != 2 {
			t.Errorf("%s: %d connections", tc.name, hellos())
		}
		l.Close()
	}
}

// ---- the listing ---------------------------------------------------------

func TestListBuildsTheListing(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	fin := testTime.Add(time.Second)
	d.setList(leadwire.ListResult{MaxRunning: 2, Running: 1, Leads: []leadwire.Lead{
		{Key: "alpha", Role: "scratch-alpha/lead", Defined: true, Provision: "done", State: "ready", Epoch: 3,
			Unit: "ataniq-lead-scratch-alpha.service", Inject: true, MessageSpent: true,
			Last: &leadwire.Receipt{CommandID: "c1", Op: leadwire.OpInject, Role: "scratch-alpha/lead", Status: leadwire.StatusRefused,
				Code: "second-message", Message: "text " + hostileMark, AdmittedAt: testTime, FinishedAt: &fin}},
		{Key: "beta", Role: "scratch-beta/lead", Provision: "none", State: "", Last: nil},
		{Key: "gamma", Role: "scratch-gamma/lead", Provision: "incomplete"},
	}})
	lst, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if lst.MaxRunning != 2 || lst.Running != 1 || len(lst.Leads) != 3 || lst.LeftOut != 0 {
		t.Fatalf("listing = %+v", lst)
	}
	a := lst.Leads[0]
	if a.Key != "alpha" || a.Role != "scratch-alpha/lead" || !a.Defined || a.Provision != "done" || a.State != "ready" || a.Epoch != 3 ||
		a.Unit != "ataniq-lead-scratch-alpha.service" || !a.Inject || !a.MessageSpent {
		t.Fatalf("alpha = %+v", a)
	}
	if a.Last == nil || a.Last.CommandID != "c1" || a.Last.Op != LeadOpInject || a.Last.Status != LeadRefused || a.Last.Code != "second-message" {
		t.Fatalf("last = %+v", a.Last)
	}
	if lst.Leads[1].Last != nil || lst.Leads[2].Provision != "incomplete" {
		t.Fatalf("rows = %+v", lst.Leads)
	}
}

// A row carries the state's reason code and the holder's three words about the
// epoch's message, each cleaned to a word of the grammar.
func TestListCarriesTheStatesReasonAndTheMessagesWords(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	d.setList(leadwire.ListResult{MaxRunning: 2, Running: 1, Leads: []leadwire.Lead{
		{Key: "alpha", Role: "scratch-alpha/lead", Defined: true, Provision: "done", State: "ready", StateReason: "holder-ready",
			Epoch: 3, Inject: true, MessageSpent: true,
			MessageResult: leadwire.MessageResult{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}},
		{Key: "beta", Role: "scratch-beta/lead", State: "stopped", StateReason: "Bad Reason\x1b[2J",
			MessageResult: leadwire.MessageResult{Outcome: "sub mitted", Reason: strings.Repeat("a", 65), Written: "whole"}},
		{Key: "gamma", Role: "scratch-gamma/lead"},
	}})
	lst, err := l.List()
	if err != nil || len(lst.Leads) != 3 {
		t.Fatalf("%+v %v", lst, err)
	}
	if a := lst.Leads[0]; a.StateReason != "holder-ready" || a.MessageResult != (LeadDelivery{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}) {
		t.Fatalf("alpha = %+v", a)
	}
	if b := lst.Leads[1]; b.StateReason != "unrecognised" || b.MessageResult != (LeadDelivery{Outcome: "unrecognised", Reason: "unrecognised", Written: "whole"}) {
		t.Fatalf("beta = %+v", b)
	}
	if g := lst.Leads[2]; g.StateReason != "" || g.MessageResult != (LeadDelivery{}) {
		t.Fatalf("gamma = %+v", g)
	}
}

// A row of a message that was not delivered is not spent and still carries the
// holder's words: the client passes both on as they are, so the view can say
// that the epoch's message is free and what became of the last one.
func TestListCarriesAFreeMessageWithTheWordsOfTheLastOne(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	d.setList(leadwire.ListResult{MaxRunning: 2, Running: 1, Leads: []leadwire.Lead{
		{Key: "alpha", Role: "scratch-alpha/lead", Defined: true, Provision: "done", State: "ready", Epoch: 3, Inject: true,
			MessageSpent: false, MessageResult: leadwire.MessageResult{Outcome: "refused", Reason: "text", Written: "nothing"}},
	}})
	lst, err := l.List()
	if err != nil || len(lst.Leads) != 1 {
		t.Fatalf("%+v %v", lst, err)
	}
	if a := lst.Leads[0]; a.MessageSpent || a.MessageResult != (LeadDelivery{Outcome: "refused", Reason: "text", Written: "nothing"}) {
		t.Fatalf("alpha = %+v", a)
	}
}

// The rule for a row of a daemon from before state_reason and message_result:
// the decoding refuses a member the types do not have and does not require one
// they have, so such a row is accepted with both empty, and a row with a member
// nobody defined is a bad answer.
func TestAListRowWithoutTheNewMembersIsAcceptedAndOneWithAnUnknownMemberIsNot(t *testing.T) {
	oldRow := map[string]any{
		"key": "alpha", "role": testRole, "defined": true, "provision": "done", "state": "ready",
		"epoch": 3, "unit": "u", "inject": true, "message_spent": true, "last": nil,
	}
	list := func(row map[string]any) map[string]any {
		return map[string]any{"max_running": 2, "running": 1, "leads": []any{row}}
	}
	p, _, _ := scriptedPeer(t, func(clientapi.Request, cmdParams) any { return list(oldRow) })
	l := newLeads(t, p.dir)
	lst, err := l.List()
	if err != nil || len(lst.Leads) != 1 {
		t.Fatalf("%+v %v", lst, err)
	}
	if r := lst.Leads[0]; r.State != "ready" || r.StateReason != "" || r.MessageResult != (LeadDelivery{}) || !r.MessageSpent {
		t.Fatalf("row = %+v", r)
	}
	l.Close()

	withUnknown := map[string]any{}
	for k, v := range oldRow {
		withUnknown[k] = v
	}
	withUnknown["state_note"] = "x"
	p2, _, _ := scriptedPeer(t, func(clientapi.Request, cmdParams) any { return list(withUnknown) })
	l2 := newLeads(t, p2.dir)
	defer l2.Close()
	if _, err := l2.List(); !isUnavailable(err, StateBadAnswer) {
		t.Fatalf("a row with an unknown member: %v", err)
	}
}

func TestAnEmptyListingIsAListing(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	lst, err := l.List()
	if err != nil || len(lst.Leads) != 0 || lst.MaxRunning != 2 {
		t.Fatalf("%+v %v", lst, err)
	}
}

func TestListCleansWhatItShowsAndKeepsRolesExact(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	d.setList(leadwire.ListResult{MaxRunning: -4, Running: -1, Leads: []leadwire.Lead{
		{Key: "al" + hostileMark + "pha\x1b[2J", Role: testRole, Provision: "Done!", State: "re‮ady\n" + strings.Repeat("x", 600), Unit: "u\x1b]0;x\x07nit"},
		{Key: "sp", Role: "scratch-sp ace/lead"},
		{Key: "nl", Role: "scratch-n\nl/lead"},
		{Key: "empty", Role: ""},
		{Key: "long", Role: strings.Repeat("r", maxLeadRole+1)},
		{Key: "uni", Role: "scratch-é/lead"},
	}})
	lst, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if lst.MaxRunning != 0 || lst.Running != 0 {
		t.Fatalf("negative counts: %+v", lst)
	}
	if len(lst.Leads) != 1 || lst.LeftOut != 5 {
		t.Fatalf("rows %d, left out %d", len(lst.Leads), lst.LeftOut)
	}
	r := lst.Leads[0]
	if r.Role != testRole {
		t.Fatalf("role = %q", r.Role)
	}
	if r.Provision != "unrecognised" {
		t.Fatalf("provision = %q", r.Provision)
	}
	for _, s := range []string{r.Key, r.State, r.Unit} {
		if strings.ContainsAny(s, "\x1b\n‮​⁦\x07") || len(s) > MaxText {
			t.Fatalf("not cleaned: %q", s)
		}
	}
}

func TestListIsBoundedToTheDaemonsCap(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	var rows []leadwire.Lead
	for i := 0; i < leadwire.MaxLeads+3; i++ {
		rows = append(rows, leadwire.Lead{Key: "k" + strconv.Itoa(i), Role: "scratch-k" + strconv.Itoa(i) + "/lead", Provision: "done"})
	}
	d.setList(leadwire.ListResult{MaxRunning: 2, Leads: rows})
	lst, err := l.List()
	if err != nil || len(lst.Leads) != leadwire.MaxLeads || lst.LeftOut != 3 {
		t.Fatalf("%d rows, left out %d, %v", len(lst.Leads), lst.LeftOut, err)
	}
}

func TestAListThatIsNotValidIsABadAnswer(t *testing.T) {
	row := func(edit func(r *leadwire.Receipt)) leadwire.ListResult {
		r := leadwire.Receipt{CommandID: "c1", Op: leadwire.OpStart, Role: testRole, Status: leadwire.StatusDone, AdmittedAt: testTime}
		edit(&r)
		return leadwire.ListResult{Leads: []leadwire.Lead{{Key: "alpha", Role: testRole, Last: &r}}}
	}
	for name, res := range map[string]any{
		"a last receipt of lead.list": row(func(r *leadwire.Receipt) { r.Op = leadwire.OpList }),
		"a last receipt of no op":     row(func(r *leadwire.Receipt) { r.Op = "" }),
		"a last receipt of another":   row(func(r *leadwire.Receipt) { r.Role = "scratch-beta/lead" }),
		"a last receipt, bad id":      row(func(r *leadwire.Receipt) { r.CommandID = "a b" }),
		"a last receipt, no id":       row(func(r *leadwire.Receipt) { r.CommandID = "" }),
		"a last receipt, bad status":  row(func(r *leadwire.Receipt) { r.Status = "nearly" }),
		"an unknown member":           map[string]any{"max_running": 2, "running": 0, "leads": []any{}, "extra": 1},
		"not an object":               []any{},
	} {
		p, _, hellos := scriptedPeer(t, func(clientapi.Request, cmdParams) any { return res })
		l := newLeads(t, p.dir)
		_, err := l.List()
		if !isUnavailable(err, StateBadAnswer) {
			t.Errorf("%s: %v", name, err)
		}
		if l.conn != nil {
			t.Errorf("%s: the connection was kept", name)
		}
		_ = hellos
		l.Close()
	}
}

func TestListErrorsArePlainStates(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	d.setFail(leadwire.OpList, clientapi.CodeInternal, "boom "+hostileMark)
	_, err := l.List()
	if !isUnavailable(err, StateUnavailable) || strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
	d.setFail(leadwire.OpList, "weird", "x")
	if _, err := l.List(); !isUnavailable(err, StateBadAnswer) {
		t.Fatalf("%v", err)
	}
	d.clearFail(leadwire.OpList)
	if _, err := l.List(); err != nil {
		t.Fatal(err)
	}
}

// ---- arguments -----------------------------------------------------------

func TestArgumentsAreCheckedBeforeAnythingIsSent(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	big := strings.Repeat("a", leadwire.MaxBody+1)
	for name, err := range map[string]func() error{
		"define, no role":        func() error { _, err := l.Define(""); return err },
		"start, a space":         func() error { _, err := l.Start("scratch a/lead"); return err },
		"stop, a long role":      func() error { _, err := l.Stop(strings.Repeat("r", maxLeadRole+1), 1); return err },
		"ticket, a newline":      func() error { _, err := l.Ticket("scratch-a\n/lead", 1, true); return err },
		"send, a bad role":       func() error { _, err := l.Send("", 1, "m1", "x"); return err },
		"send, an empty id":      func() error { _, err := l.Send(testRole, 1, "", "x"); return err },
		"send, an upper-case id": func() error { _, err := l.Send(testRole, 1, "M1", "x"); return err },
		"send, a leading dash":   func() error { _, err := l.Send(testRole, 1, "-m", "x"); return err },
		"send, a long id":        func() error { _, err := l.Send(testRole, 1, strings.Repeat("m", 65), "x"); return err },
		"send, a long body":      func() error { _, err := l.Send(testRole, 1, "m1", big); return err },
		"send, a bad body":       func() error { _, err := l.Send(testRole, 1, "m1", "a\xffb"); return err },
	} {
		if e := err(); !errors.Is(e, ErrLeadRequest) {
			t.Errorf("%s: %v", name, e)
		}
	}
	if len(d.sent()) != 0 || l.pendingCount() != 0 || l.conn != nil {
		t.Fatalf("something was sent or kept: %v, %d, %v", d.sent(), l.pendingCount(), l.conn)
	}
	// The longest valid message goes out.
	if _, err := l.Send(testRole, 1, strings.Repeat("m", 64), strings.Repeat("é", leadwire.MaxBody/2)); err != nil {
		t.Fatalf("the longest message: %v", err)
	}
	if _, err := l.Send(testRole, 1, "m1", strings.Repeat("\x01", leadwire.MaxBody)); err != nil {
		t.Fatalf("a body of control characters: %v", err)
	}
}

func TestTooManyUnconfirmedCommandsSendNothingMore(t *testing.T) {
	d := startLeadDaemon(t)
	d.setExec(inProgress)
	l := newLeads(t, d.dir)
	for i := 0; i < MaxPendingCommands; i++ {
		if _, err := l.Stop(testRole, uint64(i+1)); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
	}
	before := d.count(leadwire.OpStop)
	if _, err := l.Stop(testRole, 1000); !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("over the bound: %v", err)
	}
	if d.count(leadwire.OpStop) != before {
		t.Fatal("a command over the bound was sent")
	}
	// A command that is kept can still be repeated.
	first := d.ids(leadwire.OpStop)[0]
	r, err := l.Stop(testRole, 1)
	if err != nil || r.CommandID != first {
		t.Fatalf("repeat: %+v %v", r, err)
	}
	// Reads are not limited.
	if _, err := l.List(); err != nil {
		t.Fatal(err)
	}
}

// ---- the ends --------------------------------------------------------------

func TestCloseEndsEveryCall(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeads(t, d.dir)
	if _, err := l.List(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := l.List(); !errors.Is(err, ErrLeadsClosed) {
		t.Fatalf("List: %v", err)
	}
	if _, err := l.Start(testRole); !errors.Is(err, ErrLeadsClosed) {
		t.Fatalf("Start: %v", err)
	}
	if err := l.Detect(); !errors.Is(err, ErrLeadsClosed) {
		t.Fatalf("Detect: %v", err)
	}
	if len(d.sent()) != 1 {
		t.Fatalf("sent %v", d.sent())
	}
}

func TestCloseDuringACallReturnsClosed(t *testing.T) {
	d := startLeadDaemon(t)
	hold, release := blocker(t, leadwire.OpStart)
	d.hold = hold
	l := newLeads(t, d.dir)
	done := make(chan error, 1)
	go func() { _, err := l.Start(testRole); done <- err }()
	eventually(t, "the request to arrive", func() bool { return d.count(leadwire.OpStart) == 1 })
	l.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrLeadsClosed) {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Close")
	}
	release()
	if d.count(leadwire.OpStart) != 1 {
		t.Fatalf("a closed client sent the command again: %d", d.count(leadwire.OpStart))
	}
}

func TestErrorTextsAreFixed(t *testing.T) {
	for _, err := range []error{
		ErrNoLeadControl, ErrLeadsVersion, ErrLeadsClosed, ErrLeadRequest, ErrAnswerLost, ErrCommandConflict,
		ErrCommandUnavailable, ErrLeadNotListed, ErrLeadRejected, ErrTooManyPending, ErrNoCommandID,
	} {
		s := err.Error()
		if s == "" || len(s) > MaxText || strings.ContainsAny(s, "\x1b\n") {
			t.Errorf("%q", s)
		}
	}
}

func TestDecodeLeadJSONIsStrict(t *testing.T) {
	var r leadwire.StopResult
	for in, ok := range map[string]bool{
		`{"role":"r","state":"s","proof":"p"}`:           true,
		` {"role":"r","state":"s","proof":"p"} `:         true,
		`{"role":"r","state":"s","proof":"p"} {}`:        false,
		`{"role":"r","state":"s","proof":"p"}x`:          false,
		`{"role":"r","state":"s","proof":"p","extra":1}`: false,
		``:     false,
		`null`: true,
		`"x"`:  false,
		`[]`:   false,
	} {
		if got := decodeLeadJSON([]byte(in), &r) == nil; got != ok {
			t.Errorf("%q: %v, want %v", in, got, ok)
		}
	}
}

// A start whose answer was lost twice keeps its id, and a stop in between
// leaves the list nothing to clear it with. The next press of start replays
// the old id and gets the old done receipt: it must say that it answers an
// earlier command, and the press after it is a new command.
func TestAFinalReceiptOfAKeptIdSaysItIsEarlier(t *testing.T) {
	d := startLeadDaemon(t)
	l := newLeadsWith(t, d.dir, 200*time.Millisecond)

	// A first press that is answered is never earlier.
	fresh, err := l.Ticket(testRole, 0, false)
	if err != nil || fresh.Status != LeadDone || fresh.Earlier {
		t.Fatalf("a first answer: %+v %v", fresh, err)
	}

	// The daemon carries the start out and cannot answer, twice.
	gate := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(free)
	d.on(leadwire.OpStart, func(params json.RawMessage) (any, *clientapi.ErrorBody) {
		_, _ = d.handle(leadwire.OpStart, params)
		<-gate
		return nil, &clientapi.ErrorBody{Code: "internal", Message: "late"}
	})
	if _, err := l.Start(testRole); !errors.Is(err, ErrAnswerLost) {
		t.Fatalf("Start: %v", err)
	}
	ids := d.ids(leadwire.OpStart)
	if len(ids) != 2 || ids[0] != ids[1] || l.pendingCount() != 1 {
		t.Fatalf("ids = %v, pending %d", ids, l.pendingCount())
	}
	free()
	d.on(leadwire.OpStart, func(params json.RawMessage) (any, *clientapi.ErrorBody) { return d.handle(leadwire.OpStart, params) })

	// The owner stops the lead: the stop is its last command now, so a list
	// does not show the start's id and the client keeps it.
	stop, err := l.Stop(testRole, 0)
	if err != nil || stop.Status != LeadDone || stop.Earlier {
		t.Fatalf("Stop: %+v %v", stop, err)
	}
	d.setList(leadwire.ListResult{MaxRunning: 2, Leads: []leadwire.Lead{{
		Key: "alpha", Role: testRole, Provision: "done", State: "stopped",
		Last: &leadwire.Receipt{CommandID: stop.CommandID, Op: leadwire.OpStop, Role: testRole, Status: leadwire.StatusDone, AdmittedAt: testTime},
	}}})
	if _, err := l.List(); err != nil || l.pendingCount() != 1 {
		t.Fatalf("List: %v, pending %d", err, l.pendingCount())
	}

	// The next press of start gets the old receipt, and says so.
	old, err := l.Start(testRole)
	if err != nil || old.Status != LeadDone || old.CommandID != ids[0] || !old.Earlier {
		t.Fatalf("the press after the stop: %+v %v; want the earlier done receipt of %s", old, err, ids[0])
	}
	if d.ran(leadwire.OpStart) != 1 || l.pendingCount() != 0 {
		t.Fatalf("ran %d starts, pending %d", d.ran(leadwire.OpStart), l.pendingCount())
	}
	// The press after that is a new command, answered as itself.
	next, err := l.Start(testRole)
	if err != nil || next.Status != LeadDone || next.CommandID == ids[0] || next.Earlier {
		t.Fatalf("the next press: %+v %v", next, err)
	}
	if d.ran(leadwire.OpStart) != 2 {
		t.Fatalf("ran %d starts", d.ran(leadwire.OpStart))
	}
}
