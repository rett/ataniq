//go:build linux && ataniq_canary

package leadcanary_test

import (
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
)

// RoleOf refuses a layout name over 63 bytes, as Names does for the role in
// the other direction. The project here (63 bytes) is one leadrole accepts, so
// only the length rule of the layout name can refuse it.
func TestRoleOfRefusesALayoutNameOver63Bytes(t *testing.T) {
	over := "throwaway-" + strings.Repeat("a", 53) + "-lead"
	if len(over) != 68 {
		t.Fatalf("test name is %d bytes", len(over))
	}
	if r, err := leadcanary.RoleOf(over); err == nil {
		t.Fatalf("RoleOf(%q) gave %v", over, r)
	}
	// A layout name of exactly 63 bytes is still
	// accepted.
	ok := "throwaway-" + strings.Repeat("a", 48) + "-lead"
	if len(ok) != 63 {
		t.Fatalf("test name is %d bytes", len(ok))
	}
	if _, err := leadcanary.RoleOf(ok); err != nil {
		t.Fatalf("RoleOf(%q): %v", ok, err)
	}
}
