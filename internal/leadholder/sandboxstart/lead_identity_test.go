//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func checkLeadEnvironment(t *testing.T, w *world, env []profile.EnvVar) string {
	t.Helper()
	var lead, actor string
	for _, e := range env {
		switch e.Name {
		case profile.EnvQargiLead:
			lead = e.Value
		case profile.EnvQargiActor:
			actor = e.Value
		}
	}
	if lead != w.in.Project+"-lead-"+w.in.AuthorityTag || actor != lead+"@e1:coord:demo:1" {
		t.Fatalf("unexpected plan environment: lead %q, actor %q", lead, actor)
	}
	return lead
}

func TestManagedLeadIdentityAcrossInstances(t *testing.T) {
	var first string
	for _, tag := range []string{"0123456789abcdef", "fedcba9876543210"} {
		w := newWorld(t)
		w.in.AuthorityTag = tag
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
	for name, tag := range map[string]string{
		"missing": "", "short": "123456789abcdef", "long": "0123456789abcdef0",
		"uppercase": "0123456789abcdeF", "punctuation": "0123456789abcd-f",
		"non-hex letter": "0123456789abcdeg",
		"newline": "0123456789abcde\n", "non-ascii": "0123456789abcdé",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.in.AuthorityTag = tag
			_, err := w.run_()
			var step *StepError
			var ref *profile.Refusal
			if !errors.As(err, &ref) || ref.Field != "authority tag" || !errors.As(err, &step) || step.Step != StepValidate {
				t.Fatalf("start refusal: %v", err)
			}
			if calls := w.sys.callList(); len(calls) != 0 {
				t.Errorf("start continued after refusal: %v", calls)
			}
		})
	}
}
