package command

import (
	"errors"
	"testing"
)

func TestSenderRendering(t *testing.T) {
	cases := []struct {
		s    Sender
		want string
	}{
		{Daemon(), "ataniq"},
		{Supervisor(), "supervisor"},
		{Lead("alpha"), "lead:alpha"},
		{Sender{}, ""},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestParseSenderRoundTripsAndRefusesOwner(t *testing.T) {
	for _, s := range []Sender{Daemon(), Supervisor(), Lead("alpha"), Lead("a.b_c-9")} {
		got, err := ParseSender(s.String())
		if err != nil || got != s {
			t.Errorf("ParseSender(%q) = %v, %v; want %v", s.String(), got, err, s)
		}
	}
	if _, err := ParseSender("owner"); !errors.Is(err, ErrOwnerSender) {
		t.Errorf("ParseSender(owner) error = %v, want ErrOwnerSender", err)
	}
	for _, bad := range []string{
		"", "Owner", "OWNER", "ataniq ", "Ataniq", "lead:", "lead", "lead:Alpha", "lead:a b",
		"lead:-x", "lead:owner\n", "lead:" + string(make([]byte, 65)), "supervisor:x", "lead:lead:x",
		"\x1b", "lead:\u00e9",
	} {
		if s, err := ParseSender(bad); err == nil {
			t.Errorf("ParseSender(%q) = %v, want an error", bad, s)
		}
	}
	// "lead:owner" is a project that happens to be named owner; it is a
	// lead, never the owner.
	if s, err := ParseSender("lead:owner"); err != nil || s.String() != "lead:owner" {
		t.Errorf("ParseSender(lead:owner) = %v, %v", s, err)
	}
}

func TestNoConstructorYieldsOwner(t *testing.T) {
	for _, s := range []Sender{Daemon(), Supervisor(), Lead("owner"), Lead("")} {
		if s.String() == "owner" {
			t.Errorf("sender renders as owner: %#v", s)
		}
	}
}

func TestParseTarget(t *testing.T) {
	for _, tg := range []Target{SupervisorTarget(), LeadTarget("alpha")} {
		got, err := ParseTarget(tg.String())
		if err != nil || got != tg {
			t.Errorf("ParseTarget(%q) = %v, %v", tg.String(), got, err)
		}
	}
	for _, bad := range []string{"", "ataniq", "owner", "lead:", "lead:A", "Supervisor"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("ParseTarget(%q) succeeded", bad)
		}
	}
}

func TestValid(t *testing.T) {
	if (Sender{}).Valid() || (Target{}).Valid() {
		t.Error("zero values are valid")
	}
	if Lead("").Valid() || Lead("Bad Name").Valid() || LeadTarget("").Valid() || LeadTarget("X").Valid() {
		t.Error("a lead with a bad project is valid")
	}
	if !Daemon().Valid() || !Supervisor().Valid() || !Lead("a").Valid() || !SupervisorTarget().Valid() || !LeadTarget("a").Valid() {
		t.Error("a good value is invalid")
	}
}

func TestEdgePolicy(t *testing.T) {
	cases := []struct {
		name string
		from Sender
		to   Target
		want bool
	}{
		{"daemon to lead", Daemon(), LeadTarget("a"), true},
		{"daemon to supervisor", Daemon(), SupervisorTarget(), true},
		{"supervisor to lead", Supervisor(), LeadTarget("a"), true},
		{"supervisor to supervisor", Supervisor(), SupervisorTarget(), false},
		{"lead to supervisor", Lead("a"), SupervisorTarget(), true},
		{"lead to another lead", Lead("a"), LeadTarget("b"), false},
		{"lead to itself", Lead("a"), LeadTarget("a"), false},
		{"zero sender to lead", Sender{}, LeadTarget("a"), false},
		{"daemon to zero target", Daemon(), Target{}, false},
		{"supervisor to zero target", Supervisor(), Target{}, false},
		{"lead to zero target", Lead("a"), Target{}, false},
	}
	for _, c := range cases {
		if got := EdgeAllowed(c.from, c.to); got != c.want {
			t.Errorf("%s: EdgeAllowed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "0", "a.b", "a_b", "a-b", "x1", "a" + string(repeat('b', 63))}
	for _, s := range good {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	bad := []string{"", ".", "-a", "_a", ".a", "A", "a b", "a\n", "a]", "a=b", "\u00e9", "a\x00", "a" + string(repeat('b', 64)), "a/b", "a:b"}
	for _, s := range bad {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
}

func repeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
