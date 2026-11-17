package leadrole

import (
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

func TestParseAccepts(t *testing.T) {
	cases := []struct {
		in      string
		kind    Kind
		project string
		unit    string
		lock    string
	}{
		{"supervisor", KindSupervisor, "", "ataniq-supervisor.service", "/s/roles/supervisor.lock"},
		{"a/lead", KindLead, "a", "ataniq-lead-a.service", "/s/roles/a/lead.lock"},
		{"0/lead", KindLead, "0", "ataniq-lead-0.service", "/s/roles/0/lead.lock"},
		{"ataniq/lead", KindLead, "ataniq", "ataniq-lead-ataniq.service", "/s/roles/ataniq/lead.lock"},
		{"my-proj-2/lead", KindLead, "my-proj-2", "ataniq-lead-my-proj-2.service", "/s/roles/my-proj-2/lead.lock"},
		{"a--b/lead", KindLead, "a--b", "ataniq-lead-a--b.service", "/s/roles/a--b/lead.lock"},
		{"supervisor/lead", KindLead, "supervisor", "ataniq-lead-supervisor.service", "/s/roles/supervisor/lead.lock"},
		{"lead/lead", KindLead, "lead", "ataniq-lead-lead.service", "/s/roles/lead/lead.lock"},
		{strings.Repeat("a", 63) + "/lead", KindLead, strings.Repeat("a", 63), "ataniq-lead-" + strings.Repeat("a", 63) + ".service", "/s/roles/" + strings.Repeat("a", 63) + "/lead.lock"},
	}
	for _, c := range cases {
		r, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if r.String() != c.in || r.Kind() != c.kind || r.Project() != c.project || r.IsZero() {
			t.Errorf("Parse(%q) = %q kind %v project %q", c.in, r.String(), r.Kind(), r.Project())
		}
		if got := r.UnitName(); got != c.unit {
			t.Errorf("UnitName(%q) = %q, want %q", c.in, got, c.unit)
		}
		if got := r.LockPath("/s"); got != c.lock {
			t.Errorf("LockPath(%q) = %q, want %q", c.in, got, c.lock)
		}
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		"",
		"/",
		"/lead",
		"lead",
		"Supervisor",
		"SUPERVISOR",
		"supervisor/",
		"supervisor/lead/",
		"supervisor\n",
		" supervisor",
		"supervisor ",
		"a",
		"a/",
		"a/Lead",
		"a/leader",
		"a/lead/",
		"a/lead\n",
		" a/lead",
		"a /lead",
		"A/lead",
		"Foo/lead",
		"foo.bar/lead",
		".foo/lead",
		"foo./lead",
		"foo/bar/lead",
		"../lead",
		"a/../lead",
		"a_b/lead",
		"a b/lead",
		"-a/lead",
		"a-/lead",
		"-/lead",
		"--/lead",
		"é/lead",
		"a\x00/lead",
		"a‮/lead",
		strings.Repeat("a", 64) + "/lead",
		strings.Repeat("a", 1000) + "/lead",
		"a:b/lead",
		"a@b/lead",
		"a/lead\x00",
		"supervisor\x00",
	}
	for _, s := range bad {
		if r, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %q, want an error", s, r)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error %v does not wrap ErrInvalid", s, err)
		}
	}
}

func TestLeadProjectValidation(t *testing.T) {
	for _, p := range []string{"", "A", "a.b", "a/b", "-a", "a-", strings.Repeat("x", 64), "a b", "ü"} {
		if _, err := Lead(p); err == nil || !errors.Is(err, ErrInvalid) {
			t.Errorf("Lead(%q) error = %v", p, err)
		}
		if ValidProject(p) {
			t.Errorf("ValidProject(%q) = true", p)
		}
	}
	for _, p := range []string{"a", "z9", "a-b", strings.Repeat("x", 63)} {
		if _, err := Lead(p); err != nil {
			t.Errorf("Lead(%q): %v", p, err)
		}
		if !ValidProject(p) {
			t.Errorf("ValidProject(%q) = false", p)
		}
	}
}

func TestZeroRole(t *testing.T) {
	var r Role
	if !r.IsZero() || r.String() != "" || r.UnitName() != "" || r.LockPath("/s") != "" || r.Kind() != 0 {
		t.Fatalf("zero role is not inert: %q %q %q", r.String(), r.UnitName(), r.LockPath("/s"))
	}
	if _, err := json.Marshal(r); err == nil {
		t.Fatal("zero role marshaled")
	}
	if Kind(0).String() != "invalid" || KindLead.String() != "lead" || KindSupervisor.String() != "supervisor" {
		t.Fatal("kind names")
	}
}

func TestRolesAreComparable(t *testing.T) {
	a, _ := Lead("x")
	b, _ := Parse("x/lead")
	c, _ := Lead("y")
	if a != b || a == c || a == Supervisor() {
		t.Fatal("comparison")
	}
	m := map[Role]int{a: 1}
	if m[b] != 1 {
		t.Fatal("map key")
	}
}

func TestJSON(t *testing.T) {
	lead, _ := Lead("proj")
	b, err := json.Marshal(struct {
		A Role   `json:"a"`
		B Role   `json:"b"`
		C []Role `json:"c"`
	}{lead, Supervisor(), []Role{lead}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"proj/lead","b":"supervisor","c":["proj/lead"]}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var back struct {
		A Role   `json:"a"`
		B Role   `json:"b"`
		C []Role `json:"c"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.A != lead || back.B != Supervisor() || len(back.C) != 1 || back.C[0] != lead {
		t.Fatalf("round trip: %+v", back)
	}
	for _, bad := range []string{`null`, `1`, `true`, `[]`, `{}`, `""`, `"Foo/lead"`, `"a.b/lead"`, `"x/lead "`, `"x"`} {
		var r Role
		if err := json.Unmarshal([]byte(bad), &r); err == nil {
			t.Errorf("Unmarshal(%s) = %q, want an error", bad, r)
		}
	}
}

func randomProject(rng *rand.Rand) string {
	const alnum = "abcdefghijklmnopqrstuvwxyz0123456789"
	n := 1 + rng.Intn(MaxProjectLen)
	b := make([]byte, n)
	for i := range b {
		if i > 0 && i < n-1 && rng.Intn(5) == 0 {
			b[i] = '-'
		} else {
			b[i] = alnum[rng.Intn(len(alnum))]
		}
	}
	return string(b)
}

// TestRoundTripProperty checks that every valid role survives String, Parse
// and JSON, and that any string Parse accepts is its own canonical form.
func TestRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		var r Role
		if rng.Intn(20) == 0 {
			r = Supervisor()
		} else {
			var err error
			if r, err = Lead(randomProject(rng)); err != nil {
				t.Fatal(err)
			}
		}
		back, err := Parse(r.String())
		if err != nil || back != r {
			t.Fatalf("Parse(String(%q)) = %q, %v", r, back, err)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var viaJSON Role
		if err := json.Unmarshal(b, &viaJSON); err != nil || viaJSON != r {
			t.Fatalf("JSON round trip of %q = %q, %v", r, viaJSON, err)
		}
		if string(b) != `"`+r.String()+`"` {
			t.Fatalf("JSON %s is not the canonical string %q", b, r)
		}
		if got, want := r.UnitName(), unitOf(r); got != want {
			t.Fatalf("UnitName = %q want %q", got, want)
		}
	}
	// Random byte strings: whatever Parse accepts must be canonical.
	const alphabet = "ab09-/ lead.A\x00supervisor"
	for i := 0; i < 20000; i++ {
		n := rng.Intn(24)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(b)
		if rng.Intn(3) == 0 {
			proj := randomProject(rng)
			s = proj[:1+rng.Intn(min(3, len(proj)))] + "/lead"
			if rng.Intn(2) == 0 {
				s = strings.ToUpper(s[:1]) + s[1:]
			}
		}
		r, err := Parse(s)
		if err != nil {
			continue
		}
		if r.String() != s {
			t.Fatalf("Parse accepted %q but String gives %q", s, r.String())
		}
	}
}

func unitOf(r Role) string {
	if r.Kind() == KindSupervisor {
		return "ataniq-supervisor.service"
	}
	return "ataniq-lead-" + r.Project() + ".service"
}
