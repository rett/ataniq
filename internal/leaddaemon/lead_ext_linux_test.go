//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
)

// extFixture is the extension of the canary's one role over a registry that
// holds the role launched and shown ready, and a fake holder on the role's
// control socket. There is no daemon: the tests call the operations of the
// extension itself, with the epoch they expect.
type extFixture struct {
	*canaryEnv
	x      *canaryExt
	holder *fakeHolder
	epoch  uint64
	log    *strings.Builder
}

func newExtFixture(t *testing.T, answer holderAnswer, prefix string) *extFixture {
	t.Helper()
	c := newCanaryEnv(t)
	reg, err := leadregistry.Open(filepath.Join(c.state, RegistryDirName), leadregistry.Options{Now: c.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	if _, err := reg.Define(c.cfg.Role, leadregistry.ManagerAtaniq, profile()); err != nil {
		t.Fatal(err)
	}
	e := c.launch(reg, c.cfg.Role, leadregistry.StateReady)
	ev, err := leadcanary.OpenEvidence(c.cfg, c.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	adm, err := leadcanary.NewAdmission(c.cfg.Role)
	if err != nil {
		t.Fatal(err)
	}
	log := &strings.Builder{}
	f := &extFixture{canaryEnv: c, epoch: e.Epoch, log: log}
	f.x = &canaryExt{
		cfg: c.cfg, role: c.cfg.Role, holderDir: c.holderDir, reg: reg, ev: ev, adm: adm,
		dial: 2 * time.Second, wait: 300 * time.Millisecond, spent: map[uint64]bool{}, idPrefix: prefix,
		logf: func(format string, a ...any) { fmt.Fprintf(log, format+"\n", a...) },
	}
	f.holder = newFakeHolder(t, c.holderDir, answer)
	return f
}

// An inject or a ticket that names an epoch that is not the role's is refused
// with the code stale-epoch before anything reaches the holder, and spends
// nothing. The expected epoch that matches reaches the holder in the frame.
func TestExpectedEpochOfInjectAndTicket(t *testing.T) {
	f := newExtFixture(t, submitted, "")
	ctx := context.Background()
	stale := f.epoch + 1

	if _, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, stale); fl == nil ||
		!fl.refused || fl.code != "stale-epoch" || fl.msg != msgStaleEpoch {
		t.Fatalf("inject with a stale epoch: %+v", fl)
	}
	if _, fl := f.x.ticketFor(ctx, wire.RoleViewer, stale); fl == nil || !fl.refused || fl.code != "stale-epoch" {
		t.Fatalf("ticket with a stale epoch: %+v", fl)
	}
	if n := len(f.holder.requests("")); n != 0 {
		t.Fatalf("a request reached the holder: %d", n)
	}
	if f.x.messageSpent(f.epoch) || f.x.messageSpent(stale) {
		t.Fatal("a refused message spent an epoch")
	}
	if ev := f.lastEvidence(); ev["event"] != "inject-refused" || ev["code"] != "stale-epoch" {
		t.Fatalf("the evidence's last line %v", ev)
	}

	// The right epoch goes through, and is the epoch of the frame.
	res, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch)
	if fl != nil || res.Epoch != f.epoch {
		t.Fatalf("inject with the epoch: %+v %+v", res, fl)
	}
	if _, fl := f.x.ticketFor(ctx, wire.RoleWriter, f.epoch); fl != nil {
		t.Fatalf("ticket with the epoch: %+v", fl)
	}
	for _, r := range f.holder.requests("") {
		if r.Epoch != f.epoch {
			t.Errorf("the %s request carries epoch %d, want %d", r.Op, r.Epoch, f.epoch)
		}
	}
	// 0 is the current epoch for the canary's own calls.
	if _, fl := f.x.ticketFor(ctx, wire.RoleWriter, 0); fl != nil {
		t.Fatalf("ticket with epoch 0: %+v", fl)
	}
}

// The holder's own check of the epoch is the last guard: a role that was
// stopped and started between the check and the frame is refused by the holder,
// which the extension reports as a stale epoch for a ticket and as the holder's
// refusal for an inject (nothing was written to the lead).
func TestHolderWrongEpochIsTheLastGuard(t *testing.T) {
	f := newExtFixture(t, respondWith(wire.Response{OK: false, Code: wire.CodeWrongEpoch}), "")
	ctx := context.Background()
	if _, fl := f.x.ticketFor(ctx, wire.RoleViewer, f.epoch); fl == nil || !fl.refused || fl.code != "stale-epoch" {
		t.Fatalf("ticket: %+v", fl)
	}
	if _, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch); fl == nil ||
		fl.refused || fl.code != "holder-wrong-epoch" {
		t.Fatalf("inject: %+v", fl)
	}
}

// The command ids the daemon gives the holder begin with the prefix of the
// mode: canary- by default, lead- for the lead operations, and they stay within
// the holder's bound.
func TestHolderCommandIDPrefix(t *testing.T) {
	for prefix, want := range map[string]string{"": "canary-", "lead-": "lead-"} {
		f := newExtFixture(t, submitted, prefix)
		if _, fl := f.x.ticketFor(context.Background(), wire.RoleViewer, f.epoch); fl != nil {
			t.Fatal(fl)
		}
		reqs := f.holder.requests(wire.OpTicket)
		if len(reqs) != 1 || !strings.HasPrefix(reqs[0].CommandID, want+"ticket-") {
			t.Fatalf("prefix %q: command ids %+v", prefix, reqs)
		}
		if len(reqs[0].CommandID) > wire.MaxCommandID {
			t.Fatalf("the command id %q is over the holder's bound", reqs[0].CommandID)
		}
	}
}

// A start that was handed to the manager when the daemon was told to stop is
// not answered as "the daemon is stopping": the error that says the outcome is
// not known comes first, whatever the context says.
func TestUncertainOutcomeComesBeforeTheContext(t *testing.T) {
	x := &canaryExt{logf: func(string, ...any) {}}
	for name, tc := range map[string]struct {
		err error
		msg string
	}{
		"launch": {fmt.Errorf("%w: %w", leadlifecycle.ErrLaunchUncertain, context.Canceled),
			"the start's outcome is not known; the role is shown unknown"},
		"stop": {errors.Join(leadlifecycle.ErrStopUnproven, context.Canceled),
			"the stop is not proven; the role is shown unknown"},
		"plain context": {context.Canceled, msgStopping},
	} {
		if got := x.failure("start", tc.err); got == nil || got.Message != tc.msg {
			t.Errorf("%s: answer %+v, want %q", name, got, tc.msg)
		}
	}
}

// A role of the router that has no holder command (a lead that is not
// provisioned) has none to give the lifecycle, and gets one that the router
// then uses; the swap is safe against the lifecycle's reads.
func TestRouterHolderCommandIsOptionalAndReplaceable(t *testing.T) {
	c := newCanaryEnv(t)
	r := newLeadRouter("not here")
	in := &leadInstance{cfg: c.cfg, preflight: &seamPreflight{}}
	if err := r.add(in); err != nil {
		t.Fatal(err)
	}
	role := c.cfg.Role
	if _, _, _, err := r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{}); !errors.Is(err, errNoHolder) {
		t.Fatalf("Build without a holder command: %v", err)
	}
	h := &seamHolder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			in.setHolderCommand(h)
			in.setHolderCommand(nil)
		}
	}()
	for i := 0; i < 200; i++ {
		_, _, _, _ = r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{})
	}
	<-done
	in.setHolderCommand(h)
	if _, _, _, err := r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{}); err != nil {
		t.Fatalf("Build with a holder command: %v", err)
	}
}
