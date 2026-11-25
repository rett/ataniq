package all_test

import (
	"reflect"
	"slices"
	"testing"

	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/codex"
	"ataniq/packs/holder"
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

func TestHolderPartsMatchTerminalBuilds(t *testing.T) {
	full, parts := pack.New(), pack.New()
	if err := all.Register(full); err != nil {
		t.Fatal(err)
	}
	if err := holder.Register(parts); err != nil {
		t.Fatal(err)
	}
	var wantNames []pack.Name
	for _, name := range full.Names() {
		p, _ := full.Lookup(name)
		want := pack.Manifest{Name: name}
		for _, b := range p.Manifest().Builds {
			if slices.Contains(b.Transports, pack.Terminal) {
				want.Builds = append(want.Builds, b)
			}
		}
		if len(want.Builds) == 0 {
			continue
		}
		wantNames = append(wantNames, name)
		part, ok := parts.Lookup(name)
		if !ok {
			t.Errorf("missing holder part for %q", name)
			continue
		}
		if got := part.Manifest(); !reflect.DeepEqual(got, want) {
			t.Errorf("holder manifest for %q = %+v, want %+v", name, got, want)
		}
	}
	if got := parts.Names(); !slices.Equal(got, wantNames) {
		t.Errorf("holder parts = %v, want %v", got, wantNames)
	}
}
