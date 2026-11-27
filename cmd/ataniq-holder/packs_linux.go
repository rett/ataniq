//go:build linux

package main

import (
	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
	"ataniq/packs/holder"
)

func registeredIdentities(id buildid.Identity) ([]wire.PackIdentity, error) {
	r := pack.New()
	if err := holder.Register(r); err != nil {
		return nil, wire.RefusePack
	}
	names := r.Names()
	packs := make([]wire.PackIdentity, 0, len(names))
	if !id.Stamped() {
		// An unstamped build can display but has no exact built-in names.
		return packs, nil
	}
	// Holder parts omit the full declarations. Pin their digests in the
	// binary and check them against the source files in TestRegisteredIdentities.
	digests := map[pack.Name]string{
		"claude": "c78cee3a4cb9fa3476b47caa19784edce0203a82b9a9ac971ff47e80b5d8213e",
		"codex":  "bea377e58e41ad2f5930be8c0655b75fafc6bf4c2b50975985810c24e85db5f3",
	}
	for _, name := range names {
		full, err := pack.BuiltinIdentity(name, id.Version, id.Revision)
		if err != nil || digests[name] == "" {
			return nil, wire.RefusePack
		}
		packs = append(packs, wire.PackIdentity{Name: full, DeclarationDigest: digests[name]})
	}
	return packs, nil
}
