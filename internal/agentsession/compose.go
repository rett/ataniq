package agentsession

import (
	"bytes"
	"encoding/json"
	"slices"
)

func validateClaim(c Claim, path string) error {
	if err := enum(path+".State", c.State); err != nil {
		return err
	}
	if err := enum(path+".Basis", c.Basis); err != nil {
		return err
	}
	return enum(path+".Source", c.Source)
}
func ValidateInputs(in Inputs) error {
	if err := validateClaim(in.Transport, "Inputs.Transport"); err != nil {
		return err
	}
	if in.Self != nil {
		if err := validateClaim(*in.Self, "Inputs.Self"); err != nil {
			return err
		}
	}
	if in.Hook != nil {
		if err := validateClaim(*in.Hook, "Inputs.Hook"); err != nil {
			return err
		}
	}
	return enum("Inputs.Sight", in.Sight)
}
func (p SelfDelivery) MarshalJSON() ([]byte, error) {
	if p != "checked" {
		p = ""
	}
	return json.Marshal(string(p))
}
func stringItem(b []byte, path string, s *string) error { return value("", s).read(b, path) }
func readClaim(b []byte, path string, c *Claim) error {
	return object(b, path, value("State", &c.State), value("Basis", &c.Basis), value("Source", &c.Source), value("At", &c.At), list("Detail", &c.Detail, stringItem))
}
func optionalClaim(name string, c **Claim) field {
	return field{name, func(b json.RawMessage, path string) error {
		if bytes.Equal(b, []byte("null")) {
			return nil
		}
		*c = new(Claim)
		return readClaim(b, path, *c)
	}}
}
func DecodeInputs(b []byte) (Inputs, error) {
	var in Inputs
	err := object(b, "Inputs", value("Ended", &in.Ended), field{"Transport", func(b json.RawMessage, path string) error { return readClaim(b, path, &in.Transport) }}, optionalClaim("Self", &in.Self), optionalClaim("Hook", &in.Hook), list("Veto", (*[]string)(&in.Veto), stringItem), value("Sight", &in.Sight), field{"Policy", func(b json.RawMessage, path string) error { return object(b, path, value("Self", &in.Policy.Self)) }})
	if err == nil {
		err = ValidateInputs(in)
	}
	if err != nil {
		return Inputs{}, err
	}
	if in.Policy.Self != "checked" {
		in.Policy.Self = ""
	}
	return in, nil
}

func dropReason(slot Slot, c Claim) DroppedReason {
	if c.State == "ended" || c.State != "unknown" && (c.Basis == "host" || c.Basis == "screen" && c.State == "asking") {
		return "basis-not-allowed"
	}
	switch slot {
	case "transport":
		if c.Basis != "transport" && c.Basis != "screen" {
			return "slot-basis"
		}
		if c.Source != "" {
			return "slot-source"
		}
	case "self", "hook":
		if c.Basis != "self" {
			return "slot-basis"
		}
		if slot == "self" && c.Source == "hook" || slot == "hook" && c.Source != "hook" {
			return "slot-source"
		}
		if slot == "hook" && c.State == "asking" {
			return "hook-asking"
		}
	}
	return ""
}
func restriction(s State) int {
	switch s {
	case "blocked":
		return 4
	case "busy":
		return 3
	case "asking":
		return 2
	case "idle":
		return 1
	}
	return 0
}
func permissive(s State) bool { return s == "idle" || s == "asking" }

// Compose retains every claim for audit. Only admitted claims compete for the
// displayed state, and only the transport or a checked client can supply a permit.
func Compose(in Inputs) Record {
	if ValidateInputs(in) != nil {
		return Record{State: "unknown", Refusal: []string{"invalid input"}}
	}
	r := Record{State: "unknown", Veto: slices.Clone(in.Veto), Sight: in.Sight}
	var transport, self *Claim
	for _, entry := range [...]struct {
		slot Slot
		c    *Claim
	}{{"transport", &in.Transport}, {"self", in.Self}, {"hook", in.Hook}} {
		c := entry.c
		if c == nil {
			continue
		}
		audit := *c
		audit.Detail = slices.Clone(c.Detail)
		r.Claims = append(r.Claims, audit)
		if why := dropReason(entry.slot, *c); why != "" {
			r.Dropped = append(r.Dropped, DroppedClaim{entry.slot, why})
			continue
		}
		switch entry.slot {
		case "transport":
			transport = c
		case "self":
			self = c
		}
		if c.State == "unknown" {
			continue
		}
		if r.State != "unknown" && r.State != c.State {
			r.Disagree = true
		}
		if restriction(c.State) > restriction(r.State) {
			r.State = c.State
			r.Basis = c.Basis
		}
	}
	if in.Ended {
		r.State = "ended"
		r.Basis = "host"
	}
	r.Refusal = slices.Clone([]string(in.Veto))
	if !permissive(r.State) {
		r.Refusal = append(r.Refusal, "state "+string(r.State))
	}
	var candidate Permit
	switch {
	case transport == nil:
		r.Refusal = append(r.Refusal, "transport dropped")
	case permissive(transport.State):
		candidate = "transport"
	case transport.State == "unknown":
		if in.Policy.Self == "checked" && in.Sight == "rules" && self != nil && self.Source == "client" && self.State == "idle" {
			candidate = "self-checked"
		} else {
			r.Refusal = append(r.Refusal, "transport cannot tell")
		}
	}
	if in.Sight == "blind" {
		r.Refusal = append(r.Refusal, "untested build")
	}
	if len(r.Refusal) == 0 && candidate != "" {
		r.Deliverable = true
		r.Permit = candidate
	}
	return r
}
