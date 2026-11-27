//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadsandbox/profile"
)

func checkLeadEnvironment(t *testing.T, w *world, env []profile.EnvVar) string {
	t.Helper()
	id, err := instanceid.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	var lead, actor string
	for _, e := range env {
		switch e.Name {
		case profile.EnvQargiLead:
			lead = e.Value
		case profile.EnvQargiActor:
			actor = e.Value
		}
	}
	if lead != w.in.Project+"-lead-"+id.Authority[:16] || actor != lead+"@e1:coord:demo:1" {
		t.Fatalf("unexpected plan environment: lead %q, actor %q", lead, actor)
	}
	return lead
}

func TestManagedLeadIdentityAcrossInstances(t *testing.T) {
	var first string
	for range 2 {
		w := newWorld(t)
		w.in.AgentEnv = []profile.EnvVar{{Name: profile.EnvQargiActor, Value: sampleRole + "@e1:coord:demo:1"}}
		a := &attempt{ctx: context.Background(), in: w.in, sys: w.sys, launch: w.lch}
		t.Cleanup(func() { a.closeAll() })
		for _, step := range []func() error{a.validate, a.resolv, a.resolveSources, a.buildPlan} {
			if err := step(); err != nil {
				t.Fatal(err)
			}
		}
		lead := checkLeadEnvironment(t, w, a.pl.Env)
		if first == lead {
			t.Fatal("two instances exported the same lead name")
		}
		first = lead
	}
}

func TestManagedLeadIdentityInStartedSandboxPlan(t *testing.T) {
	w := newWorld(t)
	w.in.AgentEnv = []profile.EnvVar{{Name: profile.EnvQargiActor, Value: sampleRole + "@e1:coord:demo:1"}}
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	checkLeadEnvironment(t, w, st.Plan.Env)
}

func TestManagedLeadIdentityRefusedBeforeLaunch(t *testing.T) {
	for name, tc := range map[string]struct {
		damage func(string) error
		want   instanceid.Refusal
	}{
		"missing": {os.Remove, instanceid.ErrMissing},
		"damaged": {func(p string) error { return os.WriteFile(p, []byte("damaged"), 0600) }, instanceid.ErrInvalid},
		"unsafe":  {func(p string) error { return os.Chmod(p, 0644) }, instanceid.ErrUnsafe},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if err := tc.damage(filepath.Join(w.state, instanceid.FileName)); err != nil {
				t.Fatal(err)
			}
			_, err := w.run_()
			var step *StepError
			if !errors.Is(err, tc.want) || !errors.As(err, &step) || step.Step != StepValidate {
				t.Fatalf("start refusal: %v", err)
			}
			if calls := w.sys.callList(); len(calls) != 0 {
				t.Errorf("start continued after refusal: %v", calls)
			}
		})
	}
}
