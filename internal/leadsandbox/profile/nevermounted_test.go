package profile

import (
	"strings"
	"testing"
)

func TestCheckNeverMounted(t *testing.T) {
	never := []string{"/home/owner/.ssh", "/home/owner/work/ataniq", "/"}
	never = never[:2] // "/" is tested on its own below
	tests := []struct {
		source string
		reason string // "" for accepted
	}{
		{"/srv/state/leads/a/home", ""},
		{"/home/owner/.sshd", ""}, // a name that merely starts the same
		{"/home/owner/work/ataniq2", ""},
		{"/home/owner/work", "above"},
		{"/home/owner/.ssh", "never-mounted path"},
		{"/home/owner/.ssh/id_ed25519", "below"},
		{"/home/owner/work/ataniq/internal", "below"},
		{"/home/owner", "above"},
		{"/home", "above"},
		{"/", "above"},
		{"relative", "absolute"},
		{"/home/owner/.ssh/", "clean"},
		{"", "empty"},
	}
	for _, tt := range tests {
		err := CheckNeverMounted(tt.source, never)
		if tt.reason == "" {
			if err != nil {
				t.Errorf("%q: %v, want ok", tt.source, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q: nil, want a refusal mentioning %q", tt.source, tt.reason)
			continue
		}
		if ref := refusalOf(t, err); !strings.Contains(ref.Reason, tt.reason) {
			t.Errorf("%q: reason %q does not mention %q", tt.source, ref.Reason, tt.reason)
		}
	}
	// A never-mounted path of "/" refuses every source, and none refuses nothing.
	if err := CheckNeverMounted("/usr", []string{"/"}); err == nil {
		t.Error("everything lies below a never-mounted root")
	}
	if err := CheckNeverMounted("/usr", nil); err != nil {
		t.Errorf("no never-mounted path: %v", err)
	}
}

func TestInputsCheckSourceRefusesTheOtherRuntimeFolder(t *testing.T) {
	for _, rt := range Runtimes() {
		in := fixture(rt)
		other := "/srv/state/leads/arc-lead/config/" + string(rt.Other())
		own := "/srv/state/leads/arc-lead/config/" + string(rt)
		for _, bad := range []string{
			other,
			other + "/.credentials.json",
			"/srv/state/leads/arc-lead/config", // above it
			"/srv/state/leads/arc-lead",
			"/srv/state",
			"/home/owner/.claude",
			"/home/owner/.codex/auth.json",
		} {
			if err := in.CheckSource(bad); err == nil {
				t.Errorf("%s: source %q accepted", rt, bad)
			}
		}
		if got := in.OtherConfigDir(); got != other {
			t.Errorf("%s: OtherConfigDir = %q, want %q", rt, got, other)
		}
		// The epoch's own folder and its siblings are fine.
		for _, good := range []string{own, own + "/projects", "/srv/state/leads/arc-lead/home", "/srv/state/leads/arc-lead/tree", "/srv/state/mirrors/ataniq.git"} {
			if err := in.CheckSource(good); err != nil {
				t.Errorf("%s: source %q refused: %v", rt, good, err)
			}
		}
	}
	if got := (Inputs{}).OtherConfigDir(); got != "" {
		t.Errorf("OtherConfigDir of empty inputs = %q", got)
	}
}
