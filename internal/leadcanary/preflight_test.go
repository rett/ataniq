//go:build linux && ataniq_canary

package leadcanary_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

func TestPreflightPassesForTheConfigsRole(t *testing.T) {
	role := sampleRole(t)
	p, err := leadcanary.NewPreflight(role)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Check(context.Background(), role, leadregistry.Entry{Role: role}); err != nil {
		t.Fatalf("the canary role was refused: %v", err)
	}
}

func TestPreflightRefusesEverythingElse(t *testing.T) {
	role := sampleRole(t)
	p, err := leadcanary.NewPreflight(role)
	if err != nil {
		t.Fatal(err)
	}
	other := mustRole(t, "throwaway-two/lead")
	real := mustRole(t, "realproject/lead")
	cases := map[string]struct {
		role  leadrole.Role
		entry leadregistry.Entry
	}{
		"another throwaway role":  {other, leadregistry.Entry{Role: other}},
		"a real project":          {real, leadregistry.Entry{Role: real}},
		"the supervisor":          {leadrole.Supervisor(), leadregistry.Entry{Role: leadrole.Supervisor()}},
		"the zero role":           {leadrole.Role{}, leadregistry.Entry{}},
		"a coordinator":           {mustRole(t, "throwaway-one/coord/f"), leadregistry.Entry{Role: mustRole(t, "throwaway-one/coord/f")}},
		"role right, entry other": {role, leadregistry.Entry{Role: other}},
		"role right, entry empty": {role, leadregistry.Entry{}},
		"role other, entry right": {other, leadregistry.Entry{Role: role}},
	}
	var first string
	for name, c := range cases {
		err := p.Check(context.Background(), c.role, c.entry)
		if err == nil {
			t.Errorf("%s: the preflight passed", name)
			continue
		}
		if !errors.Is(err, leadcanary.ErrPreflightRefused) {
			t.Errorf("%s: %v does not wrap ErrPreflightRefused", name, err)
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Errorf("%s: the reason is not fixed: %q vs %q", name, err.Error(), first)
		}
		if strings.Contains(err.Error(), "realproject") || strings.Contains(err.Error(), "two") {
			t.Errorf("%s: the reason names the role: %q", name, err.Error())
		}
	}
}

func TestNewPreflightRefusesANonCanaryRole(t *testing.T) {
	for _, r := range []leadrole.Role{{}, leadrole.Supervisor(), mustRole(t, "demo/lead")} {
		if _, err := leadcanary.NewPreflight(r); err == nil {
			t.Errorf("NewPreflight accepted %v", r)
		}
	}
	var nilPre *leadcanary.Preflight
	if err := nilPre.Check(context.Background(), sampleRole(t), leadregistry.Entry{Role: sampleRole(t)}); err == nil {
		t.Error("a nil preflight passed")
	}
}

func TestPreflightRecordsNothingItself(t *testing.T) {
	// The waiver words are constants for the evidence lines; the preflight has
	// no file and no field to write them to.
	if leadcanary.QualificationText != "none (canary waiver)" || leadcanary.LoginText != "not checked" {
		t.Fatal("the evidence words are not the ones of the seams note")
	}
}
