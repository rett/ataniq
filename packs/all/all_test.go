package all_test

import (
	"slices"
	"testing"

	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/codex"
)

func TestRegister(t *testing.T) {
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	r.Freeze()
	if got, want := r.Names(), []pack.Name{"claude", "codex"}; !slices.Equal(got, want) {
		t.Fatalf("registered names = %v, want %v", got, want)
	}
	for _, name := range r.Names() {
		p, ok := r.Lookup(name)
		if !ok || p.Manifest().Name != name || len(p.Manifest().Words) == 0 {
			t.Errorf("missing full pack for %q", name)
		}
	}
}

func TestRegistrationErrors(t *testing.T) {
	r := pack.New()
	if err := r.Register(codex.Pack{}); err != nil {
		t.Fatal(err)
	}
	if err := all.Register(r); err == nil {
		t.Error("registered a duplicate pack")
	}

	r = pack.New()
	r.Freeze()
	if err := all.Register(r); err == nil {
		t.Error("registered packs in a frozen registry")
	}
}
