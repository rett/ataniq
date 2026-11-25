package pack_test

import (
	"slices"
	"strings"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

func TestParseName(t *testing.T) {
	for _, s := range []string{"ab", "a-", "x0-y", strings.Repeat("x", 40), string(packtest.Gamma)} {
		if got, err := pack.ParseName(s); err != nil || string(got) != s {
			t.Errorf("ParseName(%q) = %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "a", "-ab", "0a", "Ab", "a_b", "aé", "ab\n", strings.Repeat("x", 41)} {
		if _, err := pack.ParseName(s); err == nil {
			t.Errorf("accepted name %q", s)
		}
	}
}

type badName struct{ pack.Pack }

func (badName) Name() pack.Name { return "!" }

func TestRegistry(t *testing.T) {
	r := pack.New()
	for _, p := range []pack.Pack{packtest.Beta, packtest.Alpha} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []pack.Pack{nil, badName{}, packtest.Alpha} {
		if err := r.Register(p); err == nil {
			t.Errorf("registered invalid or duplicate pack %v", p)
		}
	}
	r.Freeze()
	if got := r.Names(); !slices.Equal(got, []pack.Name{"alpha", "beta"}) {
		t.Fatalf("Names() = %v", got)
	}
	for n, transport := range map[pack.Name]pack.Transport{"alpha": pack.Terminal, "beta": pack.Protocol} {
		p, ok := r.Lookup(n)
		if !ok || p.Name() != n || p.Manifest().Name != n ||
			!slices.Equal(p.Manifest().Builds[0].Transports, []pack.Transport{transport}) {
			t.Errorf("wrong pack for %q", n)
		}
	}
	if _, ok := r.Lookup(packtest.Gamma); ok {
		t.Fatal("gamma registered")
	}
	empty := pack.New()
	empty.Freeze()
	if err := empty.Register(packtest.Alpha); err == nil || len(empty.Names()) != 0 {
		t.Fatal("frozen registry accepted a new pack")
	}
}

func TestFrozenRegistryCopy(t *testing.T) {
	r := pack.New()
	if err := r.Register(packtest.Beta); err != nil {
		t.Fatal(err)
	}
	other := *r
	r.Freeze()
	_ = other.Register(packtest.Alpha)
	if _, ok := r.Lookup(packtest.Alpha.Name()); ok {
		t.Error("registration through a value copy changed the frozen registry")
	}
	if _, ok := r.Lookup(packtest.Beta.Name()); !ok {
		t.Error("freeze lost a registered pack")
	}
}

func TestBuiltinIdentity(t *testing.T) {
	if got, err := pack.BuiltinIdentity("alpha", "1.0.0", "abc123"); err != nil || got != "alpha@1.0.0+abc123" {
		t.Fatalf("identity = %q, %v", got, err)
	}
	if got, err := pack.BuiltinIdentity("alpha", "1.0.0+build+2", "abc123"); err != nil || got != "alpha@1.0.0+build+2+abc123" {
		t.Fatalf("identity with release metadata = %q, %v", got, err)
	}
	for _, parts := range [][3]string{
		{"!", "1", "rev"}, {"alpha", "", "rev"}, {"alpha", "1", ""}, {"alpha", "1", "a+b"}, {"alpha", "1", "a@b"},
	} {
		if _, err := pack.BuiltinIdentity(pack.Name(parts[0]), parts[1], parts[2]); err == nil {
			t.Errorf("accepted identity components %q", parts)
		}
	}
}
