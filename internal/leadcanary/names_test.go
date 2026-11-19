//go:build linux && ataniq_canary

package leadcanary_test

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadrole"
)

func TestNames(t *testing.T) {
	layoutRole, unit, err := leadcanary.Names(sampleRole(t))
	if err != nil {
		t.Fatal(err)
	}
	if layoutRole != "throwaway-one-lead" {
		t.Fatalf("layout role %q", layoutRole)
	}
	if unit != "ataniq-lead-throwaway-one.service" {
		t.Fatalf("unit %q", unit)
	}
}

func mustRole(t *testing.T, s string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNamesRoundTrip(t *testing.T) {
	for _, s := range []string{
		"throwaway-one/lead",
		"throwaway-a/lead",
		"throwaway-x-lead/lead", // the project itself ends in -lead
		"throwaway-1-2-3/lead",
		"throwaway-" + strings.Repeat("a", 48) + "/lead", // 58 bytes: the longest that fits
	} {
		role := mustRole(t, s)
		layoutRole, unit, err := leadcanary.Names(role)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if unit != role.UnitName() {
			t.Fatalf("%s: unit %q", s, unit)
		}
		back, err := leadcanary.RoleOf(layoutRole)
		if err != nil {
			t.Fatalf("%s: RoleOf(%q): %v", s, layoutRole, err)
		}
		if back != role {
			t.Fatalf("%s: round trip gave %v", s, back)
		}
	}
}

func TestNamesRefusals(t *testing.T) {
	long := "throwaway-" + strings.Repeat("a", 49) // 59 bytes: a layout name of 64
	if !leadrole.ValidProject(long) {
		t.Fatal("test project is not valid")
	}
	for name, role := range map[string]leadrole.Role{
		"zero":                 {},
		"supervisor":           leadrole.Supervisor(),
		"not throwaway":        mustRole(t, "demo/lead"),
		"prefix in the middle": mustRole(t, "my-throwaway-one/lead"),
		"coordinator":          mustRole(t, "throwaway-one/coord/feature"),
		"layout name too long": mustRole(t, long+"/lead"),
	} {
		_, _, err := leadcanary.Names(role)
		if err == nil {
			t.Errorf("%s: Names accepted the role", name)
		}
	}
	_, _, err := leadcanary.Names(mustRole(t, "demo/lead"))
	if !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Fatalf("got %v, want ErrNotCanaryRole", err)
	}
}

func TestRoleOfRefusals(t *testing.T) {
	for _, s := range []string{
		"", "-lead", "lead", "demo-lead", "throwaway-one", "throwaway-lead", "throwaway--lead",
		"throwaway-One-lead", "throwaway-one-lead-", "throwaway-one/lead", "throwaway_one-lead",
		"my-throwaway-one-lead", strings.Repeat("a", 64) + "-lead", "throwaway-one-lead\n",
	} {
		if r, err := leadcanary.RoleOf(s); err == nil {
			t.Errorf("RoleOf(%q) gave %v", s, r)
		}
	}
	_, err := leadcanary.RoleOf("demo-lead")
	if !errors.Is(err, leadcanary.ErrNotCanaryRole) {
		t.Fatalf("got %v, want ErrNotCanaryRole", err)
	}
}
