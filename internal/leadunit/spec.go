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

// The bounds that [Spec.Validate] holds a [Limits] to. They are the edges of
// what a unit may be given, not defaults: no limit value is a constant of this
// package or of the code that sets one, and each comes from configuration.
const (
	// MinMemoryMax is the least MemoryMax a unit may have: 256 MiB.
	MinMemoryMax uint64 = 256 << 20
	// MaxMemoryMax is the most MemoryMax a unit may have: 64 GiB.
	MaxMemoryMax uint64 = 64 << 30
	// MinTasksMax is the least TasksMax a unit may have: 16.
	MinTasksMax uint64 = 16
	// MaxTasksMax is the most TasksMax a unit may have: 65,536.
	MaxTasksMax uint64 = 65536
	// MinSwapMax is the least SwapMax a unit may have: 0, which means no
	// swap.
	MinSwapMax uint64 = 0
	// MaxSwapMax is the most SwapMax a unit may have: 64 GiB.
	MaxSwapMax uint64 = 64 << 30
)

// Limits are the memory, swap and task limits of a unit, in bytes and in
// tasks. A [Spec] without Limits (nil) sets none of the three properties. A
// [Spec] with Limits sets all three, so that a value of 0 is never "not set":
// a SwapMax of 0 is the limit "no swap" and renders as MemorySwapMax=0. The
// manager sets them as MemoryMax, MemorySwapMax and TasksMax in the call that
// creates the unit.
type Limits struct {
	// MemoryMax is the memory limit in bytes, from [MinMemoryMax] to
	// [MaxMemoryMax].
	MemoryMax uint64
	// SwapMax is the swap limit in bytes, from [MinSwapMax] to [MaxSwapMax];
	// 0 means no swap.
	SwapMax uint64
	// TasksMax is the limit on the number of tasks, from [MinTasksMax] to
	// [MaxTasksMax].
	TasksMax uint64
}

// Validate returns an error wrapping [ErrInvalidSpec] for a value outside the
// bounds: [Spec.Validate] holds Limits to it, and a caller that has limits
// before it has a specification can ask the same question.
func (l Limits) Validate() error {
	if l.MemoryMax < MinMemoryMax || l.MemoryMax > MaxMemoryMax {
		return fmt.Errorf("%w: memory limit %d is outside %d to %d bytes", ErrInvalidSpec, l.MemoryMax, MinMemoryMax, MaxMemoryMax)
	}
	if l.SwapMax < MinSwapMax || l.SwapMax > MaxSwapMax {
		return fmt.Errorf("%w: swap limit %d is outside %d to %d bytes", ErrInvalidSpec, l.SwapMax, MinSwapMax, MaxSwapMax)
	}
	if l.TasksMax < MinTasksMax || l.TasksMax > MaxTasksMax {
		return fmt.Errorf("%w: tasks limit %d is outside %d to %d", ErrInvalidSpec, l.TasksMax, MinTasksMax, MaxTasksMax)
	}
	return nil
}

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

// BoundProperties returns the properties that bind a unit to the unit named
// parent, in the order they are set: BindsTo, PartOf and After, each with the
// parent as its value. A unit with a [Spec.Parent] gets these in addition to
// [FixedProperties]. BindsTo makes the unit stop when the parent stops or is
// gone (and fail to start when the parent is not active), PartOf makes a stop
// or restart of the parent reach the unit, and After orders the unit's start
// after the parent's and its stop before it. The caller owns the returned
// slice. BoundProperties does not validate parent: [Spec.Validate] does.
func BoundProperties(parent string) []Property {
	return []Property{
		{Name: "BindsTo", Value: parent},
		{Name: "PartOf", Value: parent},
		{Name: "After", Value: parent},
	}
}

// LimitProperties returns the properties that carry limits, in the order they
// are set: MemoryMax, MemorySwapMax and TasksMax, each with its value as a
// decimal number of bytes or tasks. A unit with [Spec.Limits] gets these in
// addition to [FixedProperties]; a swap limit of 0 is MemorySwapMax=0, no
// swap. The caller owns the returned slice. LimitProperties does not validate
// l: [Spec.Validate] does.
func LimitProperties(l Limits) []Property {
	return []Property{
		{Name: "MemoryMax", Value: strconv.FormatUint(l.MemoryMax, 10)},
		{Name: "MemorySwapMax", Value: strconv.FormatUint(l.SwapMax, 10)},
		{Name: "TasksMax", Value: strconv.FormatUint(l.TasksMax, 10)},
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
	// Parent is the optional name of the unit this one is bound to, as for
	// the coordinator of a lead, which is bound to the lead's unit so that
	// stopping the lead stops it. It is empty for a unit that is bound to
	// nothing. When it is set the unit gets [BoundProperties] for it, and
	// it must be a unit name that [ValidateUnitName] accepts and not the
	// name of the unit itself.
	Parent string
	// Limits is the optional memory, swap and task limit of the unit. Nil sets
	// no limit property and leaves the call as it is without limits; when it
	// is given all three are set. See [Limits].
	Limits *Limits
}

// ValidateUnitName returns an error wrapping [ErrInvalidSpec] unless name is a
// unit name this package accepts: a non-empty name before a ".service" suffix,
// at most 255 bytes, made of [A-Za-z0-9:_.@-], that does not start with a dash
// (a name that does would be read as an option by a tool that takes it on a
// command line).
func ValidateUnitName(name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: unit name %q starts with a dash", ErrInvalidSpec, name)
	}
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
// no room for anything else); a Parent that [ValidateUnitName] refuses or that
// is the unit's own name; an empty or relative argv[0]; a relative or
// empty working directory; an environment entry that is not KEY=VALUE with a
// KEY of [A-Za-z_][A-Za-z0-9_]* or that repeats a key; a NUL or a newline in
// any string; any value of [Hardening]; or Limits with a value outside the
// bounds ([MinMemoryMax] to [MaxMemoryMax] bytes of memory, [MinSwapMax] to
// [MaxSwapMax] bytes of swap, [MinTasksMax] to [MaxTasksMax] tasks).
func (s Spec) Validate() error {
	if err := ValidateUnitName(s.Unit); err != nil {
		return err
	}
	if s.Parent != "" {
		if err := ValidateUnitName(s.Parent); err != nil {
			return fmt.Errorf("parent: %w", err)
		}
		if s.Parent == s.Unit {
			return fmt.Errorf("%w: unit %q is its own parent", ErrInvalidSpec, s.Unit)
		}
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
	if s.Limits != nil {
		if err := s.Limits.Validate(); err != nil {
			return err
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
	if s.Limits != nil {
		l := *s.Limits
		s.Limits = &l
	}
	return s
}
