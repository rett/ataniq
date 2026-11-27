//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// shortTB gives a world a short temporary folder: the sockets in it must fit
// the address of a Unix socket, which the name of a test would not.
type shortTB struct {
	testing.TB
	dir string
}

func (s shortTB) TempDir() string { return s.dir }

// gateManager is the unit manager of the tests of scratch mode: the fake, with
// a hook on Start that a test uses to hold a start, to lose its reply, or to
// count them.
type gateManager struct {
	leadunit.Manager
	starts atomic.Int32
	// blockInspect makes Inspect wait until its context ends, as a manager that
	// does not answer in time does.
	blockInspect atomic.Bool

	mu   sync.Mutex
	hook func(ctx context.Context, spec leadunit.Spec) (st leadunit.State, err error, handled bool)
}

func (g *gateManager) setHook(h func(ctx context.Context, spec leadunit.Spec) (leadunit.State, error, bool)) {
	g.mu.Lock()
	g.hook = h
	g.mu.Unlock()
}

func (g *gateManager) Inspect(ctx context.Context, unit string) (leadunit.State, error) {
	if g.blockInspect.Load() {
		<-ctx.Done()
		return leadunit.State{}, ctx.Err()
	}
	return g.Manager.Inspect(ctx, unit)
}

func (g *gateManager) Start(ctx context.Context, spec leadunit.Spec) (leadunit.State, error) {
	g.starts.Add(1)
	g.mu.Lock()
	h := g.hook
	g.mu.Unlock()
	if h != nil {
		if st, err, ok := h(ctx, spec); ok {
			return st, err
		}
	}
	return g.Manager.Start(ctx, spec)
}

// leadEnv is a daemon in scratch mode over a scratch world, the in-memory unit
// manager and, for the leads a test brings up, fake holders.
type leadEnv struct {
	*env
	w       *provisiontest.ScratchWorld
	gate    *gateManager
	running *running
	holders map[string]*fakeHolder
	scratch *Scratch
	n       int
}

// newLeadEnv makes the scratch world (two leads, alpha and beta, max_running 2)
// and the daemon's parts for it. Nothing is provisioned and nothing runs.
func newLeadEnv(t *testing.T) *leadEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "ls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	w := provisiontest.NewScratch(shortTB{TB: t, dir: dir})
	e := newEnv(t)
	e.state, e.runtime = w.State, w.Runtime
	initTestIdentity(t, e.state)
	e.fake = leadunit.NewFake(leadunit.SystemClock{})
	l := &leadEnv{
		env: e, w: w, gate: &gateManager{Manager: e.fake}, holders: map[string]*fakeHolder{},
	}
	l.resetScratch()
	return l
}

// resetScratch makes the daemon's Scratch from the world's config as it is now.
func (l *leadEnv) resetScratch() {
	l.scratch = &Scratch{
		Config: l.w.Config, Getenv: l.w.Getenv(),
		InjectWait: 300 * time.Millisecond, DialTimeout: 2 * time.Second,
		HolderBuild: sameHolderBuild(),
	}
}

// daemonConfig is the daemon's config for scratch mode.
func (l *leadEnv) daemonConfig() Config {
	cfg := l.env.config()
	cfg.Clock = leadunit.SystemClock{}
	cfg.NewManager = func(context.Context) (leadunit.Manager, error) { return l.gate, nil }
	cfg.Scratch = l.scratch
	return cfg
}

// start runs the daemon in scratch mode.
func (l *leadEnv) start() {
	l.t.Helper()
	l.running = l.startDaemon(l.daemonConfig())
}

// restart stops the daemon and runs it again over the same folders.
func (l *leadEnv) restart() {
	l.t.Helper()
	if err := l.running.stop(); err != nil {
		l.t.Fatalf("the daemon did not stop clean: %v", err)
	}
	l.start()
}

// key returns the config of the lead with the key.
func (l *leadEnv) cfgOf(key string) leadcanary.Config {
	l.t.Helper()
	leads, err := l.scratch.Config.Leads()
	if err != nil {
		l.t.Fatal(err)
	}
	for _, c := range leads {
		if c.Role.Project() == "scratch-"+key {
			return c
		}
	}
	l.t.Fatalf("no lead %q", key)
	return leadcanary.Config{}
}

// provision runs the provisioning of a lead as a define does, before the daemon
// runs, so that a test starts from a lead that is provisioned and not defined.
func (l *leadEnv) provision(key string) provision.ScratchResult {
	l.t.Helper()
	res, err := provision.ProvisionScratch(context.Background(), l.cfgOf(key), l.scratch.Config.Sandbox,
		provision.Options{Getenv: l.w.Getenv()})
	if err != nil {
		l.t.Fatalf("provision %s: %v", key, err)
	}
	return res
}

func roleOf(key string) string { return "scratch-" + key + "/lead" }

// holder starts the fake holder of a lead, in the holder's runtime folder that
// provisioning made.
func (l *leadEnv) holder(key string, answer holderAnswer) *fakeHolder {
	l.t.Helper()
	dir, err := l.cfgOf(key).HolderRuntimeDir()
	if err != nil {
		l.t.Fatal(err)
	}
	h := newFakeHolder(l.t, dir, answer)
	l.holders[key] = h
	return h
}

func (l *leadEnv) holderDir(key string) string {
	l.t.Helper()
	dir, err := l.cfgOf(key).HolderRuntimeDir()
	if err != nil {
		l.t.Fatal(err)
	}
	return dir
}

// call makes one call of op over the client socket and decodes the result.
func (l *leadEnv) call(op string, params, result any) error {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, l.running.socket)
	if err != nil {
		l.t.Fatal(err)
	}
	defer cl.Close()
	return cl.Call(ctx, op, params, result)
}

// id returns a fresh command id.
func (l *leadEnv) id() string {
	l.n++
	return fmt.Sprintf("cmd-%d", l.n)
}

// idN returns the command id of the i-th command of a batch.
func (l *leadEnv) idN(i int) string { return fmt.Sprintf("batch-%d", i) }

// params makes the params of a mutating operation.
func params(key, id string, extra map[string]any) map[string]any {
	p := map[string]any{"client": "test-client", "command_id": id, "role": roleOf(key)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// cmd sends a mutating operation and returns its receipt; the answer must be
// one (a receipt of any status).
func (l *leadEnv) cmd(op, key, id string, extra map[string]any) leadwire.Receipt {
	l.t.Helper()
	var r leadwire.Receipt
	if err := l.call(op, params(key, id, extra), &r); err != nil {
		l.t.Fatalf("%s: %v", op, err)
	}
	return r
}

// want checks the status and the code of a receipt.
func want(t *testing.T, r leadwire.Receipt, st leadwire.Status, code string) {
	t.Helper()
	if r.Status != st || r.Code != code {
		t.Fatalf("receipt %s %s: status %q code %q (%s), want %q %q", r.Op, r.CommandID, r.Status, r.Code, r.Message, st, code)
	}
}

// result decodes the result of a done receipt, strictly.
func result(t *testing.T, r leadwire.Receipt, v any) {
	t.Helper()
	if r.Status != leadwire.StatusDone {
		t.Fatalf("receipt %s is %q (%s %s), not done", r.Op, r.Status, r.Code, r.Message)
	}
	dec := json.NewDecoder(bytes.NewReader(r.Result))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("result %s: %v", r.Result, err)
	}
}

func (l *leadEnv) define(key string) leadwire.Receipt {
	l.t.Helper()
	return l.cmd(leadwire.OpDefine, key, l.id(), nil)
}

func (l *leadEnv) startLead(key string) leadwire.Receipt {
	l.t.Helper()
	return l.cmd(leadwire.OpStart, key, l.id(), nil)
}

func (l *leadEnv) stopLead(key string, epoch uint64) leadwire.Receipt {
	l.t.Helper()
	return l.cmd(leadwire.OpStop, key, l.id(), map[string]any{"epoch": epoch})
}

func (l *leadEnv) inject(key string, epoch uint64, msgID, body string) leadwire.Receipt {
	l.t.Helper()
	return l.cmd(leadwire.OpInject, key, l.id(), map[string]any{"epoch": epoch, "message_id": msgID, "body": body})
}

func (l *leadEnv) ticket(key string, epoch uint64, attach string) leadwire.Receipt {
	l.t.Helper()
	return l.cmd(leadwire.OpTicket, key, l.id(), map[string]any{"epoch": epoch, "attach_role": attach})
}

func (l *leadEnv) list() leadwire.ListResult {
	l.t.Helper()
	var res leadwire.ListResult
	if err := l.call(leadwire.OpList, nil, &res); err != nil {
		l.t.Fatal(err)
	}
	return res
}

// row returns the row of the lead in lead.list.
func (l *leadEnv) row(key string) leadwire.Lead {
	l.t.Helper()
	for _, r := range l.list().Leads {
		if r.Key == key {
			return r
		}
	}
	l.t.Fatalf("no row for %q", key)
	return leadwire.Lead{}
}

// up defines the lead (which provisions it), gives it a fake holder and starts
// it; both must be done. It returns the start's result.
func (l *leadEnv) up(key string, answer holderAnswer) leadwire.StartResult {
	l.t.Helper()
	d := l.define(key)
	want(l.t, d, leadwire.StatusDone, "")
	l.holder(key, answer)
	s := l.startLead(key)
	want(l.t, s, leadwire.StatusDone, "")
	var res leadwire.StartResult
	result(l.t, s, &res)
	return res
}

// tableBytes returns the command table's file.
func (l *leadEnv) tableBytes() []byte {
	l.t.Helper()
	b, err := os.ReadFile(filepath.Join(l.state, LeadCommandsName))
	if err != nil {
		l.t.Fatal(err)
	}
	return b
}

// evidenceOf returns the event names in the evidence file of the lead.
func (l *leadEnv) evidenceOf(key string) []string {
	l.t.Helper()
	b, err := os.ReadFile(l.cfgOf(key).EvidencePath())
	if err != nil {
		l.t.Fatal(err)
	}
	var out []string
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			l.t.Fatalf("evidence line %q: %v", line, err)
		}
		out = append(out, m["event"].(string))
	}
	return out
}

// bareMode is the scratch parts of a daemon without the daemon: a leadMode over
// the leads of the world's config, a registry and a command table that it
// opened in the world's state root, and an instance and an extension for each
// lead. No lifecycle controller, no socket and no locks. A test calls the
// mode's own methods; the registry and the table are closed when it ends.
func bareMode(t *testing.T, w *provisiontest.ScratchWorld) *leadMode {
	t.Helper()
	leads, err := w.Config.Leads()
	if err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	d := &daemon{Config: Config{StateDir: w.State, RuntimeDir: w.Runtime, Clock: leadunit.SystemClock{}}, log: log}
	router := newLeadRouter(msgNotALead)
	cm := &canaryMode{d: d, router: router, injectFor: 300 * time.Millisecond, dialFor: 2 * time.Second, idPrefix: idPrefixLead, build: sameHolderBuild()}
	m := &leadMode{
		cm: cm, d: d, cfg: w.Config, getenv: w.Getenv(), provisionT: time.Minute,
		roles: map[leadrole.Role]*leadRole{}, maxRunning: w.Config.MaxRunning,
	}
	cm.lead = m
	reg, err := leadregistry.Open(filepath.Join(w.State, RegistryDirName), leadregistry.Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	m.reg = reg
	tbl, err := leadcontrol.Open(filepath.Join(w.State, LeadCommandsName), leadcontrol.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tbl.Close() })
	m.table = tbl
	for i, cfg := range leads {
		pre, err := leadcanary.NewPreflightFor(leadrole.ReservedScratch, cfg.Role)
		if err != nil {
			t.Fatal(err)
		}
		in := &leadInstance{cfg: cfg, preflight: pre, spent: map[uint64]bool{}}
		if err := router.add(in); err != nil {
			t.Fatal(err)
		}
		in.ext = cm.newExt(in, nil, reg)
		m.roles[cfg.Role] = &leadRole{role: cfg.Role, cfg: cfg, key: w.Config.Entries[i].Key, in: in, op: make(chan struct{}, 1)}
	}
	return m
}

// admit admits a command of the lead to the mode's table and returns its
// receipt.
func admit(t *testing.T, m *leadMode, key, op, id string) leadcontrol.Receipt {
	t.Helper()
	lr, ok := m.lookup(roleOf(key))
	if !ok {
		t.Fatalf("no lead %q", key)
	}
	rec, fresh, err := m.table.Admit(leadcontrol.Binding{Client: "test-client", ID: id, Op: op, Role: lr.role})
	if err != nil || !fresh {
		t.Fatalf("admit: %v fresh %v", err, fresh)
	}
	return rec
}
