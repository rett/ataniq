//go:build linux

package serve

import (
	"errors"
	"io"
	"net"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
	"ataniq/packs/holder"
)

func localHolderHello() (wire.ControlHello, error) {
	id, err := buildid.Self()
	if err != nil {
		return wire.ControlHello{}, wire.RefuseBuild
	}
	packs, err := registeredIdentities(id)
	if err != nil {
		return wire.ControlHello{}, err
	}
	return wire.ControlHello{
		Version: wire.PreStartVersion,
		Range:   wire.VersionRange{Min: wire.ControlVersion, Max: wire.PreStartVersion},
		Build:   wire.HolderBuild{Module: id.Module, Version: id.Version, Revision: id.Revision, Modified: id.Modified},
		Packs:   packs,
		// This service has no hook port or session block in Status yet.
		HookFrames: []uint16{}, Session: false,
	}, nil
}

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

// preStart answers one offer and checks at most one subsequent spec. The
// selected hello lives on this connection's stack. Closing after the hello
// completes a display-only exchange.
func (s *Server) preStart(conn net.Conn, reader *wire.Reader, offer wire.ControlOffer) {
	h, err := s.cfg.holderHello()
	if err != nil {
		s.controlRefusal(conn, err)
		return
	}
	h.Selected, err = wire.NegotiateVersion(offer.Range, h.Range)
	if err != nil {
		s.controlRefusal(conn, err)
		return
	}
	h.SpecDigest = s.cfg.Launch.SpecDigest
	p, err := wire.EncodeControlHello(h)
	if err != nil {
		s.controlRefusal(conn, err)
		return
	}
	if wire.Write(conn, wire.ControlResponses, wire.TypeResponse, p) != nil {
		return
	}
	f, err := reader.Next()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.count(cntControlOther)
			s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-bad-frame"})
		}
		return
	}
	spec, err := wire.NegotiateLaunch(offer.Range, &h, s.cfg.Launch, f.Payload)
	if err != nil {
		s.controlRefusal(conn, err)
		return
	}
	if spec.Epoch != s.cfg.Epoch {
		s.count(cntControlEpoch)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "wrong-epoch"})
		s.respond(conn, refusal(wire.CodeWrongEpoch, "the request is for another epoch"))
		return
	}
	// Agreement on these bytes does not authorize starting an agent.
	s.respond(conn, wire.Response{OK: true})
}
