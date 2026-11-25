package pack

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
)

type Slot string

const (
	AgentSlot          Slot = "agent"
	ModelSlot          Slot = "model"
	EffortSlot         Slot = "effort"
	PermissionModeSlot Slot = "permission_mode"
	ResumeSlot         Slot = "resume"
)

func (s Slot) Valid() bool {
	switch s {
	case AgentSlot, ModelSlot, EffortSlot, PermissionModeSlot, ResumeSlot:
		return true
	}
	return false
}

// ModeClass is the vocabulary used by launch policy. The declaration only
// selects named modes; it cannot assign one of these classes to a mode.
type ModeClass string

const (
	Ask         ModeClass = "ask"
	Plan        ModeClass = "plan"
	AcceptEdits ModeClass = "accept-edits"
	NoPrompt    ModeClass = "no-prompt"
	Bypass      ModeClass = "bypass"
)

func (m ModeClass) Valid() bool {
	switch m {
	case Ask, Plan, AcceptEdits, NoPrompt, Bypass:
		return true
	}
	return false
}

type PermissionMode struct {
	Name string `json:"name"`
	Lead bool   `json:"lead,omitempty"`
}

type Launch struct {
	Argv            []string          `json:"argv"`
	Env             map[string]string `json:"env,omitempty"`
	Grammars        map[Slot]string   `json:"grammars,omitempty"`
	PermissionModes []PermissionMode  `json:"permission_modes,omitempty"`
	Agents          []Agent           `json:"agents,omitempty"`
	ExcludedForms   []string          `json:"excluded_forms,omitempty"` // documented forms the ceiling excludes
}

var slotCeiling = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]-]{0,63}$`)

// CheckSlotValue checks one supplied value against the declaration. Consumers
// still need the independent launch policy and the authenticated request.
func (l Launch) CheckSlotValue(slot Slot, value string) error {
	if !slotCeiling.MatchString(value) {
		return fmt.Errorf("%s: value outside slot ceiling", slot)
	}
	switch slot {
	case AgentSlot:
		if !slices.ContainsFunc(l.Agents, func(a Agent) bool { return a.Name == value }) {
			return fmt.Errorf("undeclared agent %q", value)
		}
	case PermissionModeSlot:
		if !slices.ContainsFunc(l.PermissionModes, func(m PermissionMode) bool { return m.Name == value }) {
			return fmt.Errorf("undeclared permission mode %q", value)
		}
	case EffortSlot, ModelSlot:
		rx, err := compileGrammar(l.Grammars[slot])
		if err != nil {
			return fmt.Errorf("%s grammar: %w", slot, err)
		}
		if !rx.MatchString(value) {
			return fmt.Errorf("%s value outside declared grammar", slot)
		}
	case ResumeSlot:
		// The native id's build-specific grammar belongs to the launch policy.
	default:
		return fmt.Errorf("unknown slot %q", slot)
	}
	return nil
}

var outsideCeiling = []string{
	"", "-x", "--yes", "--", ".x", "_x", ":x", "[x]", "x\ny", "x\n", "\nx",
	"x=y", "x,y", "x/y", "x y", "x'y", "x\"y", "x@y", "x\ty", "x\x00y", "xé",
	strings.Repeat("a", 65),
}

// Declaration validation checks anchors and a fixed refusal corpus. A grammar
// may admit other values outside the ceiling, so callers must still call
// CheckSlotValue for every supplied slot value.
func compileGrammar(pattern string) (*regexp.Regexp, error) {
	expr, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	if !anchored(expr, syntax.OpBeginText) || !anchored(expr, syntax.OpEndText) {
		return nil, errors.New("grammar must be anchored at both ends")
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	for _, value := range outsideCeiling {
		if rx.MatchString(value) {
			return nil, fmt.Errorf("grammar admits %q outside the ceiling", value)
		}
	}
	return rx, nil
}

// Check every alternative. Checking the first and last character of the
// pattern would accept ^abc|xyz$, an escaped dollar or multiline anchors.
func anchored(r *syntax.Regexp, end syntax.Op) bool {
	switch r.Op {
	case syntax.OpRepeat:
		return r.Min > 0 && anchored(r.Sub[0], end)
	case syntax.OpCapture, syntax.OpPlus:
		return anchored(r.Sub[0], end)
	case syntax.OpConcat:
		if end == syntax.OpBeginText {
			return anchored(r.Sub[0], end)
		}
		return anchored(r.Sub[len(r.Sub)-1], end)
	case syntax.OpAlternate:
		for _, alt := range r.Sub {
			if !anchored(alt, end) {
				return false
			}
		}
		return true
	default:
		return r.Op == end
	}
}

func tokenSlot(token string, afterSeparator bool) (Slot, error) {
	if token == "" || strings.ContainsFunc(token, unicode.IsControl) {
		return "", errors.New("empty or control-bearing argv token")
	}
	if !strings.ContainsAny(token, "{}") {
		return "", nil
	}
	prefix, name, _ := strings.Cut(token, "{")
	name, closed := strings.CutSuffix(name, "}")
	slot := Slot(name)
	if !closed || !slot.Valid() {
		return "", fmt.Errorf("unknown or malformed slot in %q", token)
	}
	if slot == ResumeSlot && prefix != "--resume=" {
		return "", errors.New("resume requires --resume={resume}")
	}
	if afterSeparator && prefix == "" {
		return slot, nil
	}
	key, suffix, eq := strings.Cut(prefix, "=")
	if !eq || key == "" || suffix != "" || strings.ContainsAny(key, "{}") {
		return "", fmt.Errorf("misplaced slot in %q", token)
	}
	return slot, nil
}

func (l Launch) validate(layout BindLayout, destinations map[string]Bind) error {
	for slot, grammar := range l.Grammars {
		if slot != ModelSlot && slot != EffortSlot {
			return fmt.Errorf("no declared grammar for slot %q", slot)
		}
		if _, err := compileGrammar(grammar); err != nil {
			return fmt.Errorf("%s grammar: %w", slot, err)
		}
	}
	modes := make(map[string]bool)
	for _, m := range l.PermissionModes {
		if !slotCeiling.MatchString(m.Name) || modes[m.Name] {
			return fmt.Errorf("invalid or repeated permission mode %q", m.Name)
		}
		modes[m.Name] = true
	}
	agents := make(map[string]bool)
	for _, a := range l.Agents {
		if !slotCeiling.MatchString(a.Name) || agents[a.Name] || !a.validSource(layout, destinations) {
			return fmt.Errorf("invalid agent name or definition for %q", a.Name)
		}
		agents[a.Name] = true
	}
	if len(l.Argv) == 0 {
		return errors.New("launch needs argv")
	}
	afterSeparator := false
	for _, token := range l.Argv {
		if token == "--" {
			afterSeparator = true
			continue
		}
		slot, err := tokenSlot(token, afterSeparator)
		if err != nil {
			return err
		}
		switch slot {
		case ModelSlot, EffortSlot:
			if l.Grammars[slot] == "" {
				return fmt.Errorf("%s slot needs a grammar", slot)
			}
		case PermissionModeSlot:
			if len(modes) == 0 {
				return errors.New("permission_mode slot needs a mode list")
			}
		case AgentSlot:
			if len(agents) == 0 {
				return errors.New("agent slot needs an agent list")
			}
		}
	}
	if len(l.Env) != 0 {
		if layout.CheckExtraEnv == nil {
			return errors.New("launch.env needs a profile environment check")
		}
		return layout.CheckExtraEnv(l.Env)
	}
	return nil
}
