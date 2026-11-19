package systemdmgr

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/leadunit"
)

// showProperties is the fixed property list of one inspection. The parser
// refuses any output that lacks one of them or has another.
const showProperties = "Id,LoadState,ActiveState,SubState,InvocationID,ControlGroup,MainPID,Description,Transient,BindsTo"

// timeoutArg renders the tool's own timeout, in whole seconds rounded up.
func timeoutArg(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return "--timeout=" + strconv.FormatInt(s, 10) + "s"
}

// showArgv is the inspection of one unit. --all makes systemctl print the
// properties whose value is empty, which the parser requires. The unit comes
// after "--" so that its name is never read as an option.
func showArgv(unit string) []string {
	return []string{toolSystemctl, "--user", "show", "--all", "--property=" + showProperties, "--", unit}
}

// userManagerGroupArgv asks for the control group of the root slice of the
// user manager, which is the manager's own control group.
func userManagerGroupArgv() []string {
	return []string{toolSystemctl, "--user", "show", "--all", "--property=ControlGroup", "--", "-.slice"}
}

// killArgv signals every process of the unit's control group.
func killArgv(unit, signal string) []string {
	return []string{toolSystemctl, "--user", "kill", "--kill-whom=all", "--signal=" + signal, "--", unit}
}

// startArgv renders the StartTransientUnit call as busctl takes it: the
// signature "ssa(sv)a(sa(sv))", the unit name, the mode "fail", the number of
// properties and each as name, variant signature and value (an array as its
// length and elements, a struct flattened), and 0 auxiliary units. Every word
// after busctl's own options is a positional word of the command, and some of
// them are caller text (the command's arguments, which may start with "-"). A
// parser that permutes would read such a word as an option wherever it stands,
// so "--" ends the options before the verb: the terminator is harmless to a
// parser that stops at the verb and protects against one that does not.
func startArgv(spec leadunit.Spec, timeout time.Duration) ([]string, error) {
	var props []string
	n := 0
	add := func(parts ...string) {
		props = append(props, parts...)
		n++
	}
	for _, p := range leadunit.FixedProperties() {
		parts, err := fixedProperty(p)
		if err != nil {
			return nil, err
		}
		add(parts...)
	}
	if spec.Parent != "" {
		for _, p := range leadunit.BoundProperties(spec.Parent) {
			parts, err := boundProperty(p)
			if err != nil {
				return nil, err
			}
			add(parts...)
		}
	}
	if spec.Limits != nil {
		for _, p := range leadunit.LimitProperties(*spec.Limits) {
			parts, err := limitProperty(p)
			if err != nil {
				return nil, err
			}
			add(parts...)
		}
	}
	add("Description", "s", spec.Description())
	add("WorkingDirectory", "s", spec.WorkDir)
	env := append([]string{"Environment", "as", strconv.Itoa(len(spec.Env))}, spec.Env...)
	add(env...)
	// ExecStartEx is a(sasas): path, argv (with argv[0]), flags. no-env-expand
	// stops systemd from expanding $VAR and ${VAR} in the arguments.
	exec := []string{"ExecStartEx", "a(sasas)", "1", spec.Argv[0], strconv.Itoa(len(spec.Argv))}
	exec = append(exec, spec.Argv...)
	exec = append(exec, "1", "no-env-expand")
	add(exec...)

	argv := []string{toolBusctl, "--user", timeoutArg(timeout), "--", "call",
		"org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager",
		"StartTransientUnit", "ssa(sv)a(sa(sv))", spec.Unit, "fail", strconv.Itoa(n)}
	argv = append(argv, props...)
	return append(argv, "0"), nil
}

// fixedProperty renders one of [leadunit.FixedProperties] as the name, the
// signature and the value the bus takes. TimeoutStopSec is TimeoutStopUSec on
// the bus, in microseconds. A property this table does not know is an error,
// so that adding one to the fixed set cannot be silently dropped from the
// call.
func fixedProperty(p leadunit.Property) ([]string, error) {
	switch p.Name {
	case "Type", "CollectMode", "KillMode", "Restart":
		return []string{p.Name, "s", p.Value}, nil
	case "NoNewPrivileges":
		switch p.Value {
		case "yes":
			return []string{p.Name, "b", "true"}, nil
		case "no":
			return []string{p.Name, "b", "false"}, nil
		}
	case "TimeoutStopSec":
		if d, err := time.ParseDuration(p.Value); err == nil && d > 0 && d%time.Microsecond == 0 {
			return []string{"TimeoutStopUSec", "t", strconv.FormatInt(d.Microseconds(), 10)}, nil
		}
	}
	return nil, fmt.Errorf("systemdmgr: no bus rendering for fixed property %s=%q", p.Name, p.Value)
}

// boundProperty renders one of [leadunit.BoundProperties] as the name, the
// signature and the value the bus takes: each is a list of unit names, "as",
// and the unit has exactly one. A property this table does not know is an
// error, as for [fixedProperty].
func boundProperty(p leadunit.Property) ([]string, error) {
	switch p.Name {
	case "BindsTo", "PartOf", "After":
		if p.Value != "" {
			return []string{p.Name, "as", "1", p.Value}, nil
		}
	}
	return nil, fmt.Errorf("systemdmgr: no bus rendering for bound property %s=%q", p.Name, p.Value)
}

// limitProperty renders one of [leadunit.LimitProperties] as the name, the
// signature and the value the bus takes: MemoryMax, MemorySwapMax and TasksMax
// are all unsigned 64-bit numbers ("t"), written in decimal. A property this
// table does not know, or a value that is not a decimal unsigned 64-bit number,
// is an error, as for [fixedProperty].
func limitProperty(p leadunit.Property) ([]string, error) {
	switch p.Name {
	case "MemoryMax", "MemorySwapMax", "TasksMax":
		if n, err := strconv.ParseUint(p.Value, 10, 64); err == nil && strconv.FormatUint(n, 10) == p.Value {
			return []string{p.Name, "t", p.Value}, nil
		}
	}
	return nil, fmt.Errorf("systemdmgr: no bus rendering for limit property %s=%q", p.Name, p.Value)
}

// parentFromBindsTo reads the BindsTo value of a show answer, a list of unit
// names separated by single spaces, as the Parent of a unit. It is that unit
// when the list has exactly one entry and [leadunit.ValidateUnitName] accepts
// it. In every other case (no entry, several, or an entry that is not a
// service unit name) it is empty: the unit is not shown to be bound to one
// parent.
func parentFromBindsTo(v string) string {
	if v == "" || strings.Contains(v, " ") {
		return ""
	}
	if leadunit.ValidateUnitName(v) != nil {
		return ""
	}
	return v
}

// snippet returns text made safe and short for an error message: printable
// ASCII only, at most 200 bytes.
func snippet(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 200 {
			b.WriteString("...")
			break
		}
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}
