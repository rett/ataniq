//go:build linux && ataniq_ownerchannel && ataniq_canary

package leaddaemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// ownerWithCanary starts a daemon in canary mode, with the owner channel's
// hook when with is set, and returns the rig that holds the owner side and the
// canary environment.
func ownerWithCanary(t *testing.T, with bool) (*canaryEnv, *ownerRig) {
	t.Helper()
	c := newCanaryEnv(t)
	r := &ownerRig{t: t, e: c.env, svc: &ownerFake{}, insp: &ownerInspect{}, logs: &syncBuffer{}}
	r.oc = &OwnerChannel{
		Build:     func() (OwnerService, func() error, error) { return r.svc, nil, nil },
		Inspector: r.insp,
	}
	cfg := c.daemonConfig()
	if with {
		cfg.ServeHook = r.oc.Serve
	}
	c.running = c.startDaemon(cfg)
	r.run = c.running
	return c, r
}

// TestOwnerAndCanaryAreTwoExtensionsOfOneProtocol: a build with both tags,
// started in canary mode with the owner-channel hook, lists the base operations,
// then the canary's five, then the owner's ten; neither set changes the
// other's answers: the canary operations answer as they do without the hook
// (the same results and the same refusals), and the owner operations answer as
// they do without canary mode.
func TestOwnerAndCanaryAreTwoExtensionsOfOneProtocol(t *testing.T) {
	both, r := ownerWithCanary(t, true)
	h := ownerHello(t, both.running.socket)
	want := append(append([]string{"hello", "status"}, CanaryOperations()...), ownerwire.Operations()...)
	if !reflect.DeepEqual(h.Operations, want) || !h.Mutations {
		t.Fatalf("operations %v, want %v", h.Operations, want)
	}

	canaryOnly, _ := ownerWithCanary(t, false)
	h2 := ownerHello(t, canaryOnly.running.socket)
	if !reflect.DeepEqual(h2.Operations, append([]string{"hello", "status"}, CanaryOperations()...)) {
		t.Fatalf("canary alone lists %v", h2.Operations)
	}

	// The same calls, the same answers.
	answers := func(c *canaryEnv) []string {
		var out []string
		var def CanaryDefineResult
		if err := c.call(OpCanaryDefine, c.role(), &def); err != nil {
			t.Fatal(err)
		}
		out = append(out, def.Role+" "+def.State)
		for _, call := range []struct {
			op     string
			params any
		}{
			{OpCanaryDefine, CanaryParams{Role: "other-one/lead"}},
			{OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}},
			{OpCanaryInject, CanaryInjectParams{Role: canaryRoleText, MessageID: "m1", Body: "hello"}},
			{OpCanaryStop, c.role()},
			{OpCanaryDefine, map[string]any{"role": canaryRoleText, "x": 1}},
		} {
			var res json.RawMessage
			switch err := c.call(call.op, call.params, &res); e := err.(type) {
			case nil:
				out = append(out, "ok: "+string(res))
			case *clientapi.Error:
				out = append(out, string(e.Code)+": "+e.Message)
			default:
				t.Fatalf("%s: %v", call.op, err)
			}
		}
		return out
	}
	a, b := answers(canaryOnly), answers(both)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the canary operations answer differently with the owner channel:\n%q\n%q", a, b)
	}
	if !strings.HasPrefix(a[0], canaryRoleText+" ") {
		t.Fatalf("define: %q", a[0])
	}

	// The owner operations work in canary mode, from the same sockets.
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	if _, err := r.decide(c, ownerFrame("ap-one")); err != nil {
		t.Fatal(err)
	}
	var listing ownerwire.Listing
	if err := c.Call(ownerCtx(t), ownerwire.OpList, nil, &listing); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.decided(); len(got) != 1 || !got[0].Src.Decider {
		t.Fatalf("%+v", got)
	}
	// And a canary request over the same connection still goes to canary.
	var def CanaryDefineResult
	if err := c.Call(ownerCtx(t), OpCanaryDefine, both.role(), &def); err != nil || def.Role != canaryRoleText {
		t.Fatalf("%+v %v", def, err)
	}
}
