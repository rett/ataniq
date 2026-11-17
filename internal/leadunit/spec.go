package leadunit

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const (
	unitSuffix   = ".service"
	maxUnitName  = 255
	maxIntentLen = 128

	descriptionPrefix = "ataniq lead epoch="
	descriptionIntent = " intent="
)

// ErrInvalidSpec is wrapped by every error that [Spec.Validate] and
// [ValidateUnitName] return.
var ErrInvalidSpec = errors.New("leadunit: invalid spec")

// Property is one unit property as a name and a value, as systemd takes it.
type Property struct {
	// Name is the property name, for example "KillMode".
	Name string
	// Value is the property value, for example "control-group".
	Value string
}

// FixedProperties returns the properties every lead unit gets, in the order
// they are set. The real manager sets exactly these (besides the description,
// the command, the working directory, the environment and the extra hardening
// of the [Spec]), and the documentation lists them from here. The caller owns
// the returned slice.
func FixedProperties() []Property {
	return []Property{
		{Name: "Type", Value: "exec"},
		{Name: "CollectMode", Value: "inactive-or-failed"},
		{Name: "KillMode", Value: "control-group"},
		{Name: "Restart", Value: "no"},
		{Name: "TimeoutStopSec", Value: "2s"},
		{Name: "NoNewPrivileges", Value: "yes"},
	}
}

// Hardening is one extra hardening property of a unit, taken from a closed
// set: only the values this package defines mean anything. The set is empty
// for now, so the zero value is the only one there is and [Spec.Validate]
// refuses it; the field exists so that a later property is a typed addition
// and never a free-form name and value.
type Hardening struct {
	kind hardeningKind
}

type hardeningKind uint8

// Property returns the unit property that the hardening sets. ok is false for
// a value that is not in the closed set.
func (h Hardening) Property() (p Property, ok bool) {
	switch h.kind {
	default:
		return Property{}, false
	}
}

// Spec is everything the manager needs to start one lead unit. Nothing is
// inherited implicitly: the environment is exactly [Spec.Env], and the working
// directory is given.
type Spec struct {
	// Unit is the unit name, for example "ataniq-lead-demo.service".
	Unit string
	// Epoch is the registry epoch of this launch. It is never 0.
	Epoch uint64
	// Intent is the launch intent id journaled before the unit call.
	Intent string
	// Argv is the command: an absolute executable path, then its arguments.
	Argv []string
	// WorkDir is the absolute working directory of the command.
	WorkDir string
	// Env is the whole environment of the command as KEY=VALUE pairs.
	Env []string
	// Hardening holds extra hardening properties beyond [FixedProperties].
	Hardening []Hardening
}

// ValidateUnitName returns an error wrapping [ErrInvalidSpec] unless name is a
// unit name this package accepts: a non-empty name before a ".service" suffix,
// at most 255 bytes, made of [A-Za-z0-9:_.@-].
func ValidateUnitName(name string) error {
	if !strings.HasSuffix(name, unitSuffix) || len(name) == len(unitSuffix) {
		return fmt.Errorf("%w: unit name %q must be a name followed by %s", ErrInvalidSpec, name, unitSuffix)
	}
	if len(name) > maxUnitName {
		return fmt.Errorf("%w: unit name is longer than %d bytes", ErrInvalidSpec, maxUnitName)
	}
	if i := strings.IndexFunc(name, func(r rune) bool { return !nameChar(r) }); i >= 0 {
		return fmt.Errorf("%w: unit name %q has a character outside [A-Za-z0-9:_.@-]", ErrInvalidSpec, name)
	}
	return nil
}

func nameChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case ':', '_', '.', '@', '-':
		return true
	}
	return false
}

func validIntent(intent string) error {
	if intent == "" {
		return fmt.Errorf("%w: intent is empty", ErrInvalidSpec)
	}
	if len(intent) > maxIntentLen {
		return fmt.Errorf("%w: intent is longer than %d bytes", ErrInvalidSpec, maxIntentLen)
	}
	if i := strings.IndexFunc(intent, func(r rune) bool { return !nameChar(r) }); i >= 0 {
		return fmt.Errorf("%w: intent %q has a character outside [A-Za-z0-9:_.@-]", ErrInvalidSpec, intent)
	}
	return nil
}

func hasNULOrNewline(s string) bool {
	return strings.ContainsAny(s, "\x00\n")
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Validate returns an error wrapping [ErrInvalidSpec] when the spec is not one
// a unit may be started from: a unit name that [ValidateUnitName] refuses; an
// epoch of 0; an intent that is empty, longer than 128 bytes or outside
// [A-Za-z0-9:_.@-] (the intent is read back from the description, which has
// no room for anything else); an empty or relative argv[0]; a relative or
// empty working directory; an environment entry that is not KEY=VALUE with a
// KEY of [A-Za-z_][A-Za-z0-9_]* or that repeats a key; a NUL or a newline in
// any string; or any value of [Hardening].
func (s Spec) Validate() error {
	if err := ValidateUnitName(s.Unit); err != nil {
		return err
	}
	if s.Epoch == 0 {
		return fmt.Errorf("%w: epoch is 0", ErrInvalidSpec)
	}
	if err := validIntent(s.Intent); err != nil {
		return err
	}
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return fmt.Errorf("%w: argv[0] is empty", ErrInvalidSpec)
	}
	if !path.IsAbs(s.Argv[0]) {
		return fmt.Errorf("%w: argv[0] %q is not an absolute path", ErrInvalidSpec, s.Argv[0])
	}
	for i, a := range s.Argv {
		if hasNULOrNewline(a) {
			return fmt.Errorf("%w: argv[%d] has a NUL or a newline", ErrInvalidSpec, i)
		}
	}
	if s.WorkDir == "" || !path.IsAbs(s.WorkDir) {
		return fmt.Errorf("%w: working directory %q is not an absolute path", ErrInvalidSpec, s.WorkDir)
	}
	if hasNULOrNewline(s.WorkDir) {
		return fmt.Errorf("%w: working directory has a NUL or a newline", ErrInvalidSpec)
	}
	seen := make(map[string]bool, len(s.Env))
	for i, kv := range s.Env {
		if hasNULOrNewline(kv) {
			return fmt.Errorf("%w: env[%d] has a NUL or a newline", ErrInvalidSpec, i)
		}
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !validEnvName(key) {
			return fmt.Errorf("%w: env[%d] is not KEY=VALUE with a valid KEY", ErrInvalidSpec, i)
		}
		if seen[key] {
			return fmt.Errorf("%w: env repeats %s", ErrInvalidSpec, key)
		}
		seen[key] = true
	}
	for i, h := range s.Hardening {
		if _, ok := h.Property(); !ok {
			return fmt.Errorf("%w: hardening[%d] is not a known hardening property", ErrInvalidSpec, i)
		}
	}
	return nil
}

// Description renders the unit description that carries the epoch and the
// intent: "ataniq lead epoch=<n> intent=<id>". [ParseDescription] reads it
// back.
func (s Spec) Description() string {
	return descriptionPrefix + strconv.FormatUint(s.Epoch, 10) + descriptionIntent + s.Intent
}

// ParseDescription reads a description that [Spec.Description] rendered. It is
// strict: the text must be exactly that form, with an epoch that is a decimal
// number from 1 up without a sign or a leading zero, and an intent that
// satisfies the rules of [Spec.Validate]. Anything else is not ours and gives
// ok false.
func ParseDescription(desc string) (epoch uint64, intent string, ok bool) {
	rest, found := strings.CutPrefix(desc, descriptionPrefix)
	if !found {
		return 0, "", false
	}
	num, intent, found := strings.Cut(rest, descriptionIntent)
	if !found || num == "" || num[0] == '0' {
		return 0, "", false
	}
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return 0, "", false
		}
	}
	epoch, err := strconv.ParseUint(num, 10, 64)
	if err != nil || epoch == 0 {
		return 0, "", false
	}
	if validIntent(intent) != nil {
		return 0, "", false
	}
	return epoch, intent, true
}

// clone returns a copy of the spec that shares no slice with it.
func (s Spec) clone() Spec {
	s.Argv = append([]string(nil), s.Argv...)
	s.Env = append([]string(nil), s.Env...)
	s.Hardening = append([]Hardening(nil), s.Hardening...)
	return s
}
