package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
)

type config struct {
	State, HomeKey, TLSDir, Listen               string
	WitnessCert, WitnessTLSPin, WitnessPublicKey string
	PendingJournal, Binding                      string
	Epoch                                        uint64
	Slot                                         cp.Slot
	TrustSources                                 link.TrustSources
}

type phase int

const (
	identityPhase phase = iota
	fullPhase
)

// Decode by exact field name so that case, duplicate, missing and type
// errors identify the field. An explicit null is never an omitted field.
type field struct {
	dst      any
	optional bool
}

func decodeObject(data []byte, name string, fields map[string]field) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("%s: expected a JSON object", name)
	}
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return fmt.Errorf("%s: invalid field name", name)
		}
		f, ok := fields[key]
		if !ok {
			return fmt.Errorf("%s: unknown field %q", name, key)
		}
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", name, key)
		}
		seen[key] = true
		var raw json.RawMessage
		if dec.Decode(&raw) != nil || string(raw) == "null" || json.Unmarshal(raw, f.dst) != nil {
			return fmt.Errorf("%s.%s: invalid value", name, key)
		}
		if s, ok := f.dst.(*string); ok && *s == "" {
			return fmt.Errorf("%s.%s: empty value", name, key)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return fmt.Errorf("%s: unfinished object", name)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s: trailing data", name)
	}
	for key, f := range fields {
		if !seen[key] && !f.optional {
			return fmt.Errorf("%s.%s: missing field", name, key)
		}
	}
	return nil
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsRune(path, 0)
}

func decodeConfig(data []byte, level phase) (config, error) {
	var c config
	if !utf8.Valid(data) {
		return c, errors.New("config: invalid UTF-8")
	}
	var epoch string
	var slot json.RawMessage
	optionalWitness := level == identityPhase
	err := decodeObject(data, "config", map[string]field{
		"state": {&c.State, false}, "home_key": {&c.HomeKey, false},
		"tls_dir": {&c.TLSDir, false}, "listen": {&c.Listen, false},
		"epoch": {&epoch, false}, "slot": {&slot, false},
		"trust_sources":   {&c.TrustSources, false},
		"pending_journal": {&c.PendingJournal, true}, "binding": {&c.Binding, true},
		"witness_cert": {&c.WitnessCert, optionalWitness}, "witness_tls_pin": {&c.WitnessTLSPin, optionalWitness},
		"witness_public_key": {&c.WitnessPublicKey, optionalWitness},
	})
	if err != nil {
		return c, err
	}
	if err := decodeObject(slot, "slot", map[string]field{
		"witness": {&c.Slot.Witness, false}, "authority": {&c.Slot.Authority, false}, "store": {&c.Slot.Store, false},
	}); err != nil {
		return c, err
	}
	for name, value := range map[string]string{"witness": c.Slot.Witness, "authority": c.Slot.Authority, "store": c.Slot.Store} {
		if len(value) > assignment.MaxReferenceBytes || strings.IndexFunc(value, func(r rune) bool { return r < ' ' || r > '~' }) >= 0 {
			return c, fmt.Errorf("slot.%s: expected a printable ASCII identifier within the witness length limit", name)
		}
	}
	c.Epoch, err = strconv.ParseUint(epoch, 10, 64)
	if err != nil || c.Epoch == 0 {
		return c, errors.New("epoch: expected a nonzero unsigned decimal string")
	}
	for name, path := range map[string]string{"state": c.State, "home_key": c.HomeKey, "tls_dir": c.TLSDir,
		"witness_cert": c.WitnessCert, "pending_journal": c.PendingJournal, "binding": c.Binding} {
		if path != "" && !absolute(path) {
			return c, fmt.Errorf("%s: expected a clean absolute path", name)
		}
	}
	ap, err := netip.ParseAddrPort(c.Listen)
	if err != nil || ap.Port() == 0 || ap.String() != c.Listen || ap.Addr().Is4In6() || ap.Addr().Zone() != "" {
		return c, errors.New("listen: expected a canonical literal IP address and nonzero port")
	}
	for name, pin := range map[string]string{"witness_public_key": c.WitnessPublicKey, "witness_tls_pin": c.WitnessTLSPin} {
		if pin != "" && (!seatgate.ValidFingerprint(pin) || pin == strings.Repeat("0", 64)) {
			return c, fmt.Errorf("%s: expected 64 lowercase hex digits for a nonzero pin", name)
		}
	}
	if c.WitnessPublicKey != "" {
		key, _ := hex.DecodeString(c.WitnessPublicKey) // checked above
		if _, err := cp.KeyID(key); err != nil {
			return c, errors.New("witness_public_key: invalid Ed25519 public key")
		}
	}
	// NewEndpoint validates TrustSources at full preflight. Apply its prefix
	// bounds during the identity ceremony too, before the peer keys exist.
	if len(c.TrustSources) == 0 {
		return c, errors.New("trust_sources: expected at least one CIDR prefix")
	}
	for _, prefix := range c.TrustSources {
		if !prefix.IsValid() || prefix.Bits() == 0 || (prefix.Addr().Is4In6() && prefix.Bits() <= 96) {
			return c, errors.New("trust_sources: invalid or unrestricted prefix")
		}
	}
	return c, nil
}

func loadConfig(path string, level phase) (config, error) {
	b, err := readFile(path, 64<<10, false)
	if err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	c, err := decodeConfig(b, level)
	if err != nil {
		return c, err
	}
	d, err := directory(c.State, true)
	if err != nil {
		return c, fmt.Errorf("state: %w", err)
	}
	return c, d.Close()
}
