package leadrole

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func TestCoordinatorParseAccepts(t *testing.T) {
	long := strings.Repeat("a", 63)
	cases := []struct {
		in, project, feature, unit, lock string
	}{
		{"p/coord/f", "p", "f", "ataniq-coord-p_f.service", "/s/roles/p/coord-f.lock"},
		{"ataniq/coord/lead-daemon", "ataniq", "lead-daemon", "ataniq-coord-ataniq_lead-daemon.service", "/s/roles/ataniq/coord-lead-daemon.lock"},
		{"a--b/coord/0", "a--b", "0", "ataniq-coord-a--b_0.service", "/s/roles/a--b/coord-0.lock"},
		{"p/coord/lead", "p", "lead", "ataniq-coord-p_lead.service", "/s/roles/p/coord-lead.lock"},
		{"p/coord/coord", "p", "coord", "ataniq-coord-p_coord.service", "/s/roles/p/coord-coord.lock"},
		{"coord/coord/coord", "coord", "coord", "ataniq-coord-coord_coord.service", "/s/roles/coord/coord-coord.lock"},
		{"supervisor/coord/supervisor", "supervisor", "supervisor", "ataniq-coord-supervisor_supervisor.service", "/s/roles/supervisor/coord-supervisor.lock"},
		{long + "/coord/" + long, long, long, "ataniq-coord-" + long + "_" + long + ".service", "/s/roles/" + long + "/coord-" + long + ".lock"},
	}
	for _, c := range cases {
		r, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if r.String() != c.in || r.Kind() != KindCoordinator || r.Project() != c.project || r.Feature() != c.feature || r.IsZero() {
			t.Errorf("Parse(%q) = %q kind %v project %q feature %q", c.in, r.String(), r.Kind(), r.Project(), r.Feature())
		}
		if got := r.UnitName(); got != c.unit {
			t.Errorf("UnitName(%q) = %q, want %q", c.in, got, c.unit)
		}
		if got := r.LockPath("/s"); got != c.lock {
			t.Errorf("LockPath(%q) = %q, want %q", c.in, got, c.lock)
		}
		built, err := Coordinator(c.project, c.feature)
		if err != nil || built != r {
			t.Errorf("Coordinator(%q, %q) = %q, %v", c.project, c.feature, built, err)
		}
	}
}

func TestCoordinatorParseRejects(t *testing.T) {
	bad := []string{
		"p/coord",
		"p/coord/",
		"/coord/f",
		"p/coord/f/",
		"p/coord/f/lead",
		"p/coord/f/coord/g",
		"p//f",
		"p/Coord/f",
		"p/coordinator/f",
		"p/coord/F",
		"P/coord/f",
		"p/coord/-f",
		"p/coord/f-",
		"p/coord/f_g",
		"p_q/coord/f",
		"p/coord/f.g",
		"p/coord/f g",
		"p/coord/ f",
		"p/coord/f\n",
		"p/coord/f\x00",
		"p/coord/é",
		"p/coord/../x",
		"p/coord/..",
		"p/coord/a:b",
		"p/coord/a@b",
		"p /coord/f",
		" p/coord/f",
		"p/coord/" + strings.Repeat("a", 64),
		strings.Repeat("a", 64) + "/coord/f",
		"coord/f",
		"p/coord/f/g",
	}
	for _, s := range bad {
		if r, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %q, want an error", s, r)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error %v does not wrap ErrInvalid", s, err)
		}
	}
	// A project named coord is a lead, not a coordinator.
	if r, err := Parse("coord/lead"); err != nil || r.Kind() != KindLead || r.Project() != "coord" {
		t.Errorf("coord/lead = %q, %v", r, err)
	}
	for _, c := range []struct{ p, f string }{{"", "f"}, {"p", ""}, {"P", "f"}, {"p", "F"}, {"p", "a_b"}, {"p", strings.Repeat("x", 64)}, {"-p", "f"}, {"p", "f-"}} {
		if _, err := Coordinator(c.p, c.f); !errors.Is(err, ErrInvalid) {
			t.Errorf("Coordinator(%q, %q) error = %v", c.p, c.f, err)
		}
	}
	for _, f := range []string{"", "A", "a_b", "-a", "a-", strings.Repeat("x", 64), "a.b"} {
		if ValidFeature(f) {
			t.Errorf("ValidFeature(%q) = true", f)
		}
	}
	for _, f := range []string{"a", "z9", "lead-daemon", strings.Repeat("x", 63)} {
		if !ValidFeature(f) {
			t.Errorf("ValidFeature(%q) = false", f)
		}
	}
}

func TestCoordinatorParent(t *testing.T) {
	c, _ := Coordinator("proj", "feat")
	want, _ := Lead("proj")
	if got := c.Parent(); got != want || got.Kind() != KindLead {
		t.Fatalf("Parent = %q", got)
	}
	l, _ := Lead("proj")
	for _, r := range []Role{l, Supervisor(), {}} {
		if !r.Parent().IsZero() {
			t.Errorf("Parent of %q = %q, want the zero role", r, r.Parent())
		}
	}
	if KindCoordinator.String() != "coordinator" {
		t.Fatal("kind name")
	}
}

func TestCoordinatorJSON(t *testing.T) {
	c, _ := Coordinator("proj", "feat")
	b, err := json.Marshal(struct{ R Role }{c})
	if err != nil || string(b) != `{"R":"proj/coord/feat"}` {
		t.Fatalf("got %s, %v", b, err)
	}
	var back struct{ R Role }
	if err := json.Unmarshal(b, &back); err != nil || back.R != c {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	for _, bad := range []string{`"proj/coord/"`, `"proj/coord/F"`, `"proj/coord/a_b"`, `"proj/coord"`} {
		var r Role
		if err := json.Unmarshal([]byte(bad), &r); err == nil {
			t.Errorf("Unmarshal(%s) = %q, want an error", bad, r)
		}
	}
}

func TestIdentity(t *testing.T) {
	c, _ := Coordinator("proj", "lead-daemon")
	if got, want := c.Identity(3, 7), "proj-lead@e3:coord:lead-daemon:7"; got != want {
		t.Fatalf("Identity = %q, want %q", got, want)
	}
	if got := c.Identity(math.MaxUint64, math.MaxUint64); got != "proj-lead@e18446744073709551615:coord:lead-daemon:18446744073709551615" {
		t.Fatalf("Identity max = %q", got)
	}
	l, _ := Lead("proj")
	for _, r := range []Role{l, Supervisor(), {}} {
		if r.Identity(1, 1) != "" {
			t.Errorf("Identity of %q = %q, want empty", r, r.Identity(1, 1))
		}
	}
	// A zero epoch or instance has no identity: ParseIdentity refuses it, so
	// Identity gives "" and every identity it returns parses back.
	for _, zero := range [][2]uint64{{0, 1}, {1, 0}, {0, 0}} {
		if got := c.Identity(zero[0], zero[1]); got != "" {
			t.Errorf("Identity(%d, %d) = %q, want empty", zero[0], zero[1], got)
		}
	}
	role, pe, n, err := ParseIdentity("proj-lead@e3:coord:lead-daemon:7")
	if err != nil || role != c || pe != 3 || n != 7 {
		t.Fatalf("ParseIdentity = %q %d %d %v", role, pe, n, err)
	}
	// A project that ends in -lead and one that holds "coord" still parse
	// whole.
	for _, c := range []struct{ project, feature string }{{"a-lead", "f"}, {"coord", "coord"}, {"lead", "lead"}, {"x-lead-lead", "y"}} {
		r, _ := Coordinator(c.project, c.feature)
		back, pe, n, err := ParseIdentity(r.Identity(1, 2))
		if err != nil || back != r || pe != 1 || n != 2 {
			t.Errorf("%q: ParseIdentity = %q %d %d %v", r.Identity(1, 2), back, pe, n, err)
		}
	}
}

func TestParseIdentityRejects(t *testing.T) {
	bad := []string{
		"",
		"proj-lead@e3:coord:f:7 ",
		" proj-lead@e3:coord:f:7",
		"proj-lead@e3:coord:f:7\n",
		"proj-lead@e3:coord:f:7:",
		"proj-lead@e3:coord:f",
		"proj-lead@e3:coord:f:7:8",
		"proj-lead@e3:coord::7",
		"proj-lead@e3:coordx:f:7",
		"proj-lead@e3:lead:f:7",
		"proj-lead@3:coord:f:7",
		"proj-lead@e:coord:f:7",
		"proj-lead@E3:coord:f:7",
		"proj-lead@e03:coord:f:7",
		"proj-lead@e0:coord:f:7",
		"proj-lead@e3:coord:f:0",
		"proj-lead@e3:coord:f:07",
		"proj-lead@e+3:coord:f:7",
		"proj-lead@e-3:coord:f:7",
		"proj-lead@e3 :coord:f:7",
		"proj-lead@e3:coord:f:7x",
		"proj-lead@e3:coord:f:0x7",
		"proj-lead@e3:coord:f:1e3",
		"proj-lead@e18446744073709551616:coord:f:7",
		"proj-lead@e3:coord:f:18446744073709551616",
		"proj-lead@e3:coord:F:7",
		"proj-lead@e3:coord:f_g:7",
		"proj-lead@e3:coord:-f:7",
		"proj-lead@e3:coord:f/g:7",
		"proj-lead@e3:coord:..:7",
		"proj@e3:coord:f:7",
		"proj-leader@e3:coord:f:7",
		"-lead@e3:coord:f:7",
		"Proj-lead@e3:coord:f:7",
		"pro_j-lead@e3:coord:f:7",
		"a@b-lead@e3:coord:f:7",
		"proj-lead@e3@e4:coord:f:7",
		"proj-lead@",
		"proj-lead@e",
		"@e3:coord:f:7",
		"proj-lead",
		"proj/lead@e3:coord:f:7",
		"proj-lead@e3:coord:f:7\x00",
		strings.Repeat("a", 64) + "-lead@e3:coord:f:7",
		"proj-lead@e3:coord:" + strings.Repeat("a", 64) + ":7",
	}
	for _, s := range bad {
		if r, pe, n, err := ParseIdentity(s); err == nil {
			t.Errorf("ParseIdentity(%q) = %q %d %d, want an error", s, r, pe, n)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseIdentity(%q) error %v does not wrap ErrInvalid", s, err)
		}
	}
}

func randomCoordinator(rng *rand.Rand) Role {
	r, err := Coordinator(randomProject(rng), randomProject(rng))
	if err != nil {
		panic(err)
	}
	return r
}

// TestCoordinatorRoundTripProperty: every valid coordinator survives String,
// Parse, JSON and its identity, and whatever ParseIdentity accepts is its own
// canonical form.
func TestCoordinatorRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 5000; i++ {
		r := randomCoordinator(rng)
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
		pe, n := 1+rng.Uint64()>>uint(rng.Intn(64)), 1+rng.Uint64()>>uint(rng.Intn(64))
		if pe == 0 || n == 0 { // 1+MaxUint64 wrapped
			continue
		}
		id := r.Identity(pe, n)
		gotRole, gotPE, gotN, err := ParseIdentity(id)
		if err != nil || gotRole != r || gotPE != pe || gotN != n {
			t.Fatalf("ParseIdentity(%q) = %q %d %d %v", id, gotRole, gotPE, gotN, err)
		}
		if gotRole.Identity(gotPE, gotN) != id {
			t.Fatalf("identity %q is not canonical", id)
		}
		if r.Parent().Parent() != (Role{}) || r.Parent().Project() != r.Project() {
			t.Fatalf("Parent of %q = %q", r, r.Parent())
		}
	}
	// Random strings: whatever ParseIdentity accepts is canonical.
	const alphabet = "ab09-@e:coordlead_ /+"
	for i := 0; i < 50000; i++ {
		b := make([]byte, rng.Intn(30))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(b)
		if rng.Intn(3) == 0 {
			s = "a-lead@e" + string("0123456789"[rng.Intn(10)]) + ":coord:" + s[:min(len(s), 3)] + ":" + string("0123456789"[rng.Intn(10)])
		}
		role, pe, n, err := ParseIdentity(s)
		if err != nil {
			continue
		}
		if role.Identity(pe, n) != s {
			t.Fatalf("ParseIdentity accepted %q but the canonical form is %q", s, role.Identity(pe, n))
		}
	}
}

// TestUnitAndLockUnambiguous: no lead, supervisor or coordinator of any valid
// names shares a unit name or a lock path with a different role, and a unit
// name reads back to exactly one role.
func TestUnitAndLockUnambiguous(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var roles []Role
	roles = append(roles, Supervisor())
	// Names chosen to collide if the mapping were loose: words that appear in
	// the fixed parts of the unit names and lock paths.
	words := []string{"a", "lead", "coord", "supervisor", "ataniq", "x-lead", "coord-a", "a-coord", "roles", "b-c", "0"}
	for _, p := range words {
		l, err := Lead(p)
		if err != nil {
			t.Fatal(err)
		}
		roles = append(roles, l)
		for _, f := range words {
			c, err := Coordinator(p, f)
			if err != nil {
				t.Fatal(err)
			}
			roles = append(roles, c)
		}
	}
	for i := 0; i < 1500; i++ {
		l, _ := Lead(randomProject(rng))
		roles = append(roles, l, randomCoordinator(rng))
	}
	units := map[string]Role{}
	locks := map[string]Role{}
	canon := map[string]Role{}
	for _, r := range roles {
		if prev, ok := canon[r.String()]; ok && prev != r {
			t.Fatalf("%q and %q share a canonical string", prev, r)
		}
		canon[r.String()] = r
		u, l := r.UnitName(), r.LockPath("/s")
		if prev, ok := units[u]; ok && prev != r {
			t.Fatalf("%q and %q share the unit %q", prev, r, u)
		}
		units[u] = r
		if prev, ok := locks[l]; ok && prev != r {
			t.Fatalf("%q and %q share the lock %q", prev, r, l)
		}
		locks[l] = r
	}
	// A coordinator unit name splits at its one underscore.
	for _, r := range roles {
		if r.Kind() != KindCoordinator {
			if strings.Contains(r.UnitName(), "_") {
				t.Fatalf("unit %q of %q has an underscore", r.UnitName(), r)
			}
			continue
		}
		body, ok := strings.CutPrefix(r.UnitName(), "ataniq-coord-")
		if !ok {
			t.Fatalf("unit %q", r.UnitName())
		}
		body = strings.TrimSuffix(body, ".service")
		if strings.Count(body, "_") != 1 {
			t.Fatalf("unit %q does not hold exactly one underscore", r.UnitName())
		}
		project, feature, _ := strings.Cut(body, "_")
		if project != r.Project() || feature != r.Feature() {
			t.Fatalf("unit %q does not read back to %q", r.UnitName(), r)
		}
		if !strings.HasPrefix(r.UnitName(), "ataniq-coord-") || strings.HasPrefix(r.UnitName(), "ataniq-lead-") {
			t.Fatalf("unit %q has a lead prefix", r.UnitName())
		}
	}
}
