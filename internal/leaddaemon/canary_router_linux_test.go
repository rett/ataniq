//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

const (
	routerRoleOne   = canaryRoleText
	routerRoleTwo   = "throwaway-two/lead"
	routerRoleOther = "throwaway-three/lead"
)

func mustRole(t *testing.T, text string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// seamPreflight and seamHolder are the seams of one instance that count their
// calls, so that a test sees which instance the router asked.
type seamPreflight struct{ calls []leadrole.Role }

func (p *seamPreflight) Check(_ context.Context, role leadrole.Role, _ leadregistry.Entry) error {
	p.calls = append(p.calls, role)
	return nil
}

type seamHolder struct{ calls []leadrole.Role }

func (h *seamHolder) Build(role leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) ([]string, string, []string, error) {
	h.calls = append(h.calls, role)
	return []string{"/opt/sample/holder"}, "/srv/sample", []string{"SAMPLE=1"}, nil
}

// The router answers all three seams of the lifecycle for the roles it has, by
// asking the role's own instance, and refuses every other role in each.
func TestRouterRefusesARoleThatIsNotListed(t *testing.T) {
	c := newCanaryEnv(t)
	one := mustRole(t, routerRoleOne)
	two := mustRole(t, routerRoleTwo)
	other := mustRole(t, routerRoleOther)
	cfg2 := c.cfg
	cfg2.Role = two
	cfg2.Limits.TasksMax = 77
	pre1, pre2 := &seamPreflight{}, &seamPreflight{}
	hold1, hold2 := &seamHolder{}, &seamHolder{}
	r := newLeadRouter("not here")
	if err := r.add(&leadInstance{cfg: c.cfg, preflight: pre1, holder: hold1}); err != nil {
		t.Fatal(err)
	}
	if err := r.add(&leadInstance{cfg: cfg2, preflight: pre2, holder: hold2}); err != nil {
		t.Fatal(err)
	}
	if err := r.add(&leadInstance{cfg: cfg2}); err == nil {
		t.Fatal("a second instance of one role was added")
	}
	if err := r.add(&leadInstance{}); err == nil {
		t.Fatal("an instance without a role was added")
	}
	if len(r.order) != 2 || r.order[0] != one || r.order[1] != two {
		t.Fatalf("order %v", r.order)
	}

	t.Run("listed roles reach their own instance", func(t *testing.T) {
		ctx := context.Background()
		if err := r.Check(ctx, two, leadregistry.Entry{Role: two}); err != nil {
			t.Fatal(err)
		}
		if len(pre1.calls) != 0 || len(pre2.calls) != 1 {
			t.Fatalf("preflight calls %v and %v", pre1.calls, pre2.calls)
		}
		if _, _, _, err := r.Build(one, leadregistry.Entry{Role: one}, leadregistry.Intent{}); err != nil {
			t.Fatal(err)
		}
		if len(hold1.calls) != 1 || len(hold2.calls) != 0 {
			t.Fatalf("holder calls %v and %v", hold1.calls, hold2.calls)
		}
		if l := r.Limits(one); l == nil || l.TasksMax != c.cfg.Limits.TasksMax {
			t.Fatalf("limits of the first role %+v", l)
		}
		if l := r.Limits(two); l == nil || l.TasksMax != 77 {
			t.Fatalf("limits of the second role %+v", l)
		}
	})

	t.Run("a role that is not listed is refused by each seam", func(t *testing.T) {
		err := r.Check(context.Background(), other, leadregistry.Entry{Role: other})
		if !errors.Is(err, leadcanary.ErrPreflightRefused) {
			t.Fatalf("Check: %v", err)
		}
		if argv, dir, env, err := r.Build(other, leadregistry.Entry{Role: other}, leadregistry.Intent{}); err == nil || argv != nil || dir != "" || env != nil {
			t.Fatalf("Build: %v %q %v %v", argv, dir, env, err)
		}
		if l := r.Limits(other); l != nil {
			t.Fatalf("Limits: %+v", l)
		}
		if l := r.Limits(leadrole.Role{}); l != nil {
			t.Fatalf("Limits of the zero role: %+v", l)
		}
		if err := r.Check(context.Background(), leadrole.Role{}, leadregistry.Entry{}); !errors.Is(err, leadcanary.ErrPreflightRefused) {
			t.Fatalf("Check of the zero role: %v", err)
		}
		if len(pre1.calls)+len(pre2.calls) != 1 || len(hold1.calls)+len(hold2.calls) != 1 {
			t.Fatal("an instance was asked about a role that is not its own")
		}
	})

	t.Run("the operations refuse it after the params are found well formed", func(t *testing.T) {
		in1 := r.byRole[one]
		in1.ext = &canaryExt{role: one}
		x := &routerExt{router: r}
		for op, params := range map[string]string{
			OpCanaryDefine: `{"role":"` + routerRoleOther + `"}`,
			OpCanaryStart:  `{"role":"` + routerRoleOther + `"}`,
			OpCanaryStop:   `{"role":"` + routerRoleOther + `"}`,
			OpCanaryTicket: `{"role":"` + routerRoleOther + `","attach_role":"nonsense"}`,
			OpCanaryInject: `{"role":"` + routerRoleOther + `","message_id":"msg-one","body":"b"}`,
		} {
			got, f := x.Handle(context.Background(), op, json.RawMessage(params))
			if got != nil || f == nil || f.Code != CodeRefused || f.Message != "not here" {
				t.Errorf("%s: %v, %+v", op, got, f)
			}
			// The same with a role that is not a role at all, and with one
			// that is a role only after a normalisation.
			for _, text := range []string{"", "nonsense", "THROWAWAY-ONE/LEAD", " " + routerRoleOne} {
				p := json.RawMessage(`{"role":` + jsonText(t, text) + `}`)
				if op == OpCanaryTicket {
					p = json.RawMessage(`{"role":` + jsonText(t, text) + `,"attach_role":"writer"}`)
				}
				if op == OpCanaryInject {
					p = json.RawMessage(`{"role":` + jsonText(t, text) + `,"message_id":"msg-one","body":"b"}`)
				}
				if _, f := x.Handle(context.Background(), op, p); f == nil || f.Code != CodeRefused || f.Message != "not here" {
					t.Errorf("%s with the role %q: %+v", op, text, f)
				}
			}
		}
		// A malformed request is a malformed request, whatever the role.
		for _, params := range []string{``, `[]`, `{"role":1}`, `{"role":"` + routerRoleOther + `","extra":1}`} {
			if _, f := x.Handle(context.Background(), OpCanaryStart, json.RawMessage(params)); f == nil || f.Code != clientapi.CodeInvalidRequest {
				t.Errorf("params %q: %+v", params, f)
			}
		}
		if _, f := x.Handle(context.Background(), "canary.nonsense", json.RawMessage(`{}`)); f == nil || f.Code != clientapi.CodeUnsupportedOperation {
			t.Errorf("an unknown operation: %+v", f)
		}
	})
}

func jsonText(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// twoRoles is a daemon's canary mode with two roles in its router, over the
// in-memory unit manager and one fake control server of the holder for each.
type twoRoles struct {
	t       *testing.T
	c       *canaryEnv
	cfgs    [2]leadcanary.Config
	holders [2]string
	mode    *canaryMode
	reg     *leadregistry.Registry
	ctl     *leadlifecycle.Controller
	ext     *routerExt
}

// newTwoRoles builds the two instances (each with its own state root, so each
// has its own evidence file), opens their evidence, builds the lifecycle over
// the router and the extension. The registry is also given a third role that
// the router does not have.
func newTwoRoles(t *testing.T, c *canaryEnv) *twoRoles {
	t.Helper()
	w := &twoRoles{t: t, c: c}
	w.cfgs[0] = c.cfg
	w.cfgs[1] = c.cfg
	w.cfgs[1].Role = mustRole(t, routerRoleTwo)
	w.cfgs[1].StateRoot = filepath.Join(c.base, "state-two")
	if err := os.Mkdir(w.cfgs[1].StateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range w.cfgs {
		dir, err := w.cfgs[i].HolderRuntimeDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		w.holders[i] = dir
	}
	w.mode = w.buildMode()
	if err := w.mode.openEvidence(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.mode.closeEvidence() })

	reg, err := leadregistry.Open(filepath.Join(c.state, RegistryDirName), leadregistry.Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	opts := leadlifecycle.Options{Registry: reg, Manager: c.fake, Clock: leadunit.SystemClock{}, Preflight: refusePreflight{}, Holder: refuseHolder{}}
	w.mode.configure(&opts)
	ctl, err := leadlifecycle.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	w.reg, w.ctl = reg, ctl
	exts := w.mode.extensions(ctl, reg)
	if len(exts) != 1 {
		t.Fatalf("%d extensions", len(exts))
	}
	w.ext = exts[0].(*routerExt)
	return w
}

// buildMode makes the mode of the two configs, as canarySetup makes the one of
// the canary's.
func (w *twoRoles) buildMode() *canaryMode {
	w.t.Helper()
	d := &daemon{Config: w.c.daemonConfig(), log: io.Discard}
	r := newLeadRouter(msgNotCanary)
	for _, cfg := range w.cfgs {
		pre, err := leadcanary.NewPreflight(cfg.Role)
		if err != nil {
			w.t.Fatal(err)
		}
		hold, err := newHolderCommand(cfg, leadlock.DaemonPath(w.c.state))
		if err != nil {
			w.t.Fatal(err)
		}
		if err := r.add(&leadInstance{cfg: cfg, preflight: pre, holder: hold}); err != nil {
			w.t.Fatal(err)
		}
	}
	return &canaryMode{d: d, router: r, injectFor: 10 * time.Second, dialFor: 2 * time.Second}
}

// call makes one call of op on the router's extension.
func (w *twoRoles) call(op string, params any) (any, *clientapi.ErrorBody) {
	w.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.ext.Handle(context.Background(), op, raw)
}

// up defines and starts role i, through the router's seams.
func (w *twoRoles) up(i int) uint64 {
	w.t.Helper()
	role := w.cfgs[i].Role
	if _, f := w.call(OpCanaryDefine, CanaryParams{Role: role.String()}); f != nil {
		w.t.Fatalf("define: %+v", f)
	}
	e, err := w.ctl.Start(context.Background(), role)
	if err != nil {
		w.t.Fatalf("start: %v", err)
	}
	return e.Epoch
}

func (w *twoRoles) inject(i int, id string) (any, *clientapi.ErrorBody) {
	w.t.Helper()
	return w.call(OpCanaryInject, CanaryInjectParams{Role: w.cfgs[i].Role.String(), MessageID: id, Body: "body text\n"})
}

// The lifecycle over the router starts a role of the router and refuses a role
// of the registry that the router does not have, with nothing started for it.
func TestRouterLifecycleStartsOnlyItsRoles(t *testing.T) {
	c := newCanaryEnv(t)
	w := newTwoRoles(t, c)
	if got, want := w.up(0), uint64(1); got != want {
		t.Fatalf("epoch %d", got)
	}
	if got, want := w.up(1), uint64(1); got != want {
		t.Fatalf("epoch %d", got)
	}
	if n := len(c.fake.StartedSpecs()); n != 2 {
		t.Fatalf("%d units started, want 2", n)
	}
	other := mustRole(t, routerRoleOther)
	if _, err := w.reg.Define(other, leadregistry.ManagerAtaniq, w.ext.router.byRole[mustRole(t, routerRoleOne)].ext.profile); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ctl.Start(context.Background(), other); !errors.Is(err, leadlifecycle.ErrPreflight) {
		t.Fatalf("start of a role that is not the router's: %v", err)
	}
	if n := len(c.fake.StartedSpecs()); n != 2 {
		t.Fatalf("%d units started after the refusal, want 2", n)
	}
	// The operations refuse it too.
	if _, f := w.call(OpCanaryStart, CanaryParams{Role: routerRoleOther}); f == nil || f.Code != CodeRefused || f.Message != msgNotCanary {
		t.Fatalf("canary.start of a role that is not the router's: %+v", f)
	}
}

// With two roles, a message in flight to one role does not refuse a message to
// the other, and the spent epoch of one role is not the other's.
func TestRouterInjectInFlightIsPerRole(t *testing.T) {
	c := newCanaryEnv(t)
	w := newTwoRoles(t, c)
	started, release := make(chan struct{}, 1), make(chan struct{})
	h1 := newFakeHolder(t, w.holders[0], func(req wire.Request, conn net.Conn) {
		if req.Op == wire.OpInject {
			started <- struct{}{}
			<-release
		}
		submitted(req, conn)
	})
	h2 := newFakeHolder(t, w.holders[1], submitted)
	e1, e2 := w.up(0), w.up(1)

	type answer struct {
		got any
		f   *clientapi.ErrorBody
	}
	first := make(chan answer, 1)
	go func() {
		got, f := w.inject(0, "msg-one")
		first <- answer{got, f}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first message did not reach the first holder")
	}

	// The first role is in flight, and says so to a second message.
	if _, f := w.inject(0, "msg-two"); f == nil || f.Code != CodeRefused || f.Message != msgInFlight {
		t.Fatalf("a second message to the role in flight: %+v", f)
	}
	// The second role takes its message at once.
	got, f := w.inject(1, "msg-three")
	if f != nil {
		t.Fatalf("a message to the other role: %+v", f)
	}
	if res, ok := got.(CanaryInjectResult); !ok || res.Role != routerRoleTwo || res.Epoch != e2 || res.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("result %+v", got)
	}
	close(release)
	select {
	case a := <-first:
		if a.f != nil {
			t.Fatalf("the first message: %+v", a.f)
		}
		if res, ok := a.got.(CanaryInjectResult); !ok || res.Role != routerRoleOne || res.Epoch != e1 {
			t.Fatalf("result %+v", a.got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first message did not end")
	}

	// Each role is spent for its own epoch, and each answers a second message
	// with the refusal of the second message.
	for i := range w.cfgs {
		if _, f := w.inject(i, "msg-four"); f == nil || f.Message != msgSecond {
			t.Fatalf("a second message to role %d: %+v", i, f)
		}
	}
	if n := len(h1.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d messages reached the first holder", n)
	}
	if n := len(h2.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d messages reached the second holder", n)
	}
}

// Both roles are in epoch 1, and a message sent to one does not spend the
// other's: the spent epochs are kept per role, in memory and in the evidence
// the next daemon reads back.
func TestRouterSpentEpochsArePerRole(t *testing.T) {
	c := newCanaryEnv(t)
	w := newTwoRoles(t, c)
	h1 := newFakeHolder(t, w.holders[0], submitted)
	h2 := newFakeHolder(t, w.holders[1], submitted)
	if e1, e2 := w.up(0), w.up(1); e1 != 1 || e2 != 1 {
		t.Fatalf("epochs %d and %d", e1, e2)
	}
	if _, f := w.inject(0, "msg-one"); f != nil {
		t.Fatalf("the first role: %+v", f)
	}
	x1, x2 := w.ext.router.byRole[w.cfgs[0].Role].ext, w.ext.router.byRole[w.cfgs[1].Role].ext
	x1.mu.Lock()
	spent1, spent2 := x1.spent[1], x2.spent[1]
	x1.mu.Unlock()
	if !spent1 || spent2 {
		t.Fatalf("after one message to the first role: spent %v and %v", spent1, spent2)
	}
	if _, f := w.inject(1, "msg-two"); f != nil {
		t.Fatalf("the second role, with the first one's epoch spent: %+v", f)
	}
	if n := len(h2.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d messages reached the second holder", n)
	}
	if _, f := w.inject(1, "msg-three"); f == nil || f.Message != msgSecond {
		t.Fatalf("a second message to the second role: %+v", f)
	}
	if n := len(h1.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d messages reached the first holder", n)
	}

	// Each role's evidence file shows its own message and no other.
	for i, cfg := range w.cfgs {
		other := w.cfgs[1-i].Role.String()
		spent, err := spentEpochs(cfg.EvidencePath(), cfg.Role.String())
		if err != nil || len(spent) != 1 || !spent[1] {
			t.Fatalf("role %d: spent %v, err %v", i, spent, err)
		}
		if spent, err := spentEpochs(cfg.EvidencePath(), other); err != nil || len(spent) != 0 {
			t.Fatalf("role %d's file shows the messages of the other: %v, %v", i, spent, err)
		}
	}

	// A daemon that starts next reads each role's epochs from its own file.
	if err := w.mode.closeEvidence(); err != nil {
		t.Fatal(err)
	}
	next := w.buildMode()
	if err := next.openEvidence(); err != nil {
		t.Fatal(err)
	}
	defer next.closeEvidence()
	for i, cfg := range w.cfgs {
		in := next.router.byRole[cfg.Role]
		if len(in.spent) != 1 || !in.spent[1] {
			t.Fatalf("role %d read back %v", i, in.spent)
		}
	}
}

// A file that cannot be read back refuses the start of the whole mode and
// leaves no evidence file open: the instances opened before it are closed.
func TestRouterOpenEvidenceClosesWhatItOpenedOnAFailure(t *testing.T) {
	c := newCanaryEnv(t)
	w := newTwoRoles(t, c)
	if err := w.mode.closeEvidence(); err != nil {
		t.Fatal(err)
	}
	// A line that is part of a line, in the second role's file.
	if err := os.WriteFile(w.cfgs[1].EvidencePath(), []byte(`{"event":"inject-sent"`), 0o600); err != nil {
		t.Fatal(err)
	}
	next := w.buildMode()
	if err := next.openEvidence(); err == nil {
		t.Fatal("a damaged file was read back")
	}
	for _, cfg := range w.cfgs {
		if in := next.router.byRole[cfg.Role]; in.ev != nil {
			t.Fatalf("the evidence of %s is still open", cfg.Role)
		}
	}
}
