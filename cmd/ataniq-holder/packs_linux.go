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
	for _, name := range names {
		full, err := pack.BuiltinIdentity(name, id.Version, id.Revision)
		digest, ok := holder.DeclarationDigest(name)
		if err != nil || !ok {
			return nil, wire.RefusePack
		}
		packs = append(packs, wire.PackIdentity{Name: full, DeclarationDigest: digest})
	}
	return packs, nil
}
