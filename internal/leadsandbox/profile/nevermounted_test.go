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
}

func TestCheckNeverMountedFailsClosed(t *testing.T) {
	// An empty list protects nothing: refuse.
	for _, never := range [][]string{nil, {}} {
		err := CheckNeverMounted("/usr", never)
		if err == nil {
			t.Fatalf("an empty list %#v accepted a source", never)
		}
		if ref := refusalOf(t, err); ref.Field != "never-mounted paths" || !strings.Contains(ref.Reason, "none given") {
			t.Errorf("refusal = {%q, %q}", ref.Field, ref.Reason)
		}
	}
	// An entry that is not clean never matches by equality or prefix, so the
	// check refuses the entry itself, whatever the source is.
	sources := []string{
		"/home/owner/.ssh", "/home/owner/.ssh/id_ed25519", "/home/owner", "/srv/state/leads/a/home",
	}
	entries := []struct{ entry, reason string }{
		{"/home/owner/.ssh/", "clean"},
		{"/home/owner//.ssh", "clean"},
		{"/home/owner/./.ssh", "clean"},
		{"/home/owner/x/../.ssh", "clean"},
		{"home/owner/.ssh", "absolute"},
		{"", "empty"},
		{"/home/owner/\x00.ssh", "control"},
	}
	for _, e := range entries {
		for _, src := range sources {
			// The unclean entry is listed after a good one: its index is 1, and
			// it is refused before any entry is compared with the source.
			err := CheckNeverMounted(src, []string{"/home/owner/work/ataniq", e.entry})
			if err == nil {
				t.Errorf("entry %q, source %q: accepted", e.entry, src)
				continue
			}
			ref := refusalOf(t, err)
			if ref.Field != "never-mounted path 1" || !strings.Contains(ref.Reason, e.reason) {
				t.Errorf("entry %q, source %q: refusal {%q, %q}, want never-mounted path 1 with %q", e.entry, src, ref.Field, ref.Reason, e.reason)
			}
		}
	}
	// A clean list still works.
	if err := CheckNeverMounted("/usr", []string{"/home/owner/.ssh"}); err != nil {
		t.Errorf("clean list: %v", err)
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

// CheckSource holds a path that did not come from the table (the real path of a
// host row) to the rules of a source of the table: never-mounted paths, the
// other runtime's config folder, other leads' state and other projects'
// mirror folders.
func TestCheckSourceHoldsAnyPathToTheRulesOfTheTable(t *testing.T) {
	in := fixture(Claude)
	for _, ok := range []string{
		"/usr", "/etc/localtime", "/usr/share/zoneinfo/UTC", "/opt/x",
		"/srv/state/leads/arc-lead/home", "/srv/state/leads/arc-lead/gitmeta/config",
		"/srv/state/mirrors/ataniq.git", "/srv/state/mirrors/ataniq.git/refs",
	} {
		if err := in.CheckSource(ok); err != nil {
			t.Errorf("CheckSource(%s) = %v", ok, err)
		}
	}
	for source, want := range map[string]string{
		"/home/owner/.ssh": "never-mounted",
		"/home/owner":      "never-mounted",
		"/srv/state/leads/arc-lead/config/codex/x": "never-mounted",
		"/srv/state/leads/other-lead/home":         "another lead's state",
		"/srv/state/leads":                         "never-mounted", // above the other runtime's config folder
		"/srv/state":                               "never-mounted",
		"/srv/state/leads/arc-lead2":               "another lead's state",
		"/srv/state/mirrors/other.git":             "another project's mirror folder",
		"/srv/state/mirrors":                       "another project's mirror folder",
		"relative":                                 "absolute",
	} {
		err := in.CheckSource(source)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckSource(%s) = %v, want a refusal that says %q", source, err, want)
		}
	}
}
