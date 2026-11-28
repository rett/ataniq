//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
	"ataniq/packs/holder"
)

func TestRegisteredIdentities(t *testing.T) {
	build := buildid.Identity{Version: "v1.2.3", Revision: "abc123"}
	got, err := registeredIdentities(build)
	if err != nil {
		t.Fatal(err)
	}
	r := pack.New()
	if err := holder.Register(r); err != nil {
		t.Fatal(err)
	}
	var want []wire.PackIdentity
	for _, name := range r.Names() {
		files := map[string][]byte{}
		for _, path := range []string{pack.ManifestPath, pack.HooksPath, pack.SandboxPath} {
			data, err := os.ReadFile(filepath.Join("../../packs", string(name), path))
			if path != pack.ManifestPath && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			files[path] = data
		}
		digest, err := pack.DeclarationDigest(files)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, wire.PackIdentity{Name: string(name) + "@v1.2.3+abc123", DeclarationDigest: digest})
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("registered identities: got %+v, want %+v", got, want)
	}
}
