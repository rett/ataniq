package plan

import (
	"strings"

	"ataniq/internal/leadsandbox/profile"
)

// emitter builds the bubblewrap arguments. Every operand it is given is
// checked. bubblewrap reads operands by position, so an operand that starts
// with "--" would be taken as a value, not as an option; the plan refuses it
// anyway so that its parse and its rendering stay unambiguous, and a host path
// is absolute, so the refusal costs nothing.
type emitter struct {
	args []string
	err  error
}

// raw appends arguments the profile gives (its flag set) or the terminator.
func (e *emitter) raw(a ...string) { e.args = append(e.args, a...) }

// opt appends an option and its operands.
func (e *emitter) opt(name string, operands ...string) {
	e.args = append(e.args, name)
	for _, o := range operands {
		if strings.HasPrefix(o, "--") && e.err == nil {
			e.err = refuse("bubblewrap argument", "an operand of %s starts with --, which the plan refuses so that its parse stays unambiguous", name)
		}
		e.args = append(e.args, o)
	}
}

// bind appends the option of one row of the bind table.
func (e *emitter) bind(b profile.Bind) {
	switch b.Kind {
	case profile.KindTmpfs:
		e.opt("--tmpfs", b.Inside)
	case profile.KindDev:
		e.opt("--dev", b.Inside)
	case profile.KindProc:
		e.opt("--proc", b.Inside)
	case profile.KindRoBind:
		e.opt("--ro-bind", b.Source, b.Inside)
	case profile.KindRwBind:
		e.opt("--bind", b.Source, b.Inside)
	case profile.KindSymlink:
		e.opt("--symlink", b.Target, b.Inside)
	default:
		if e.err == nil {
			e.err = refuse("bind "+b.Inside, "no bubblewrap option for the kind %q", b.Kind)
		}
	}
}

// clip shortens a note for a problem; the caller formats it with %q.
func clip(s string) string {
	const max = 64
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
