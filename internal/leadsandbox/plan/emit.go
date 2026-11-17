package plan

import (
	"strings"

	"ataniq/internal/leadsandbox/profile"
)

// emitter builds the bubblewrap arguments. Every operand it is given is
// checked: bubblewrap reads an argument that starts with "--" as an option in
// some positions, and a host path is absolute, so a refusal costs nothing.
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
			e.err = refuse("bubblewrap argument", "%q would start with -- and be read as an option", clip(o))
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

// clip shortens a value for a refusal or a problem and quotes nothing: the
// caller formats it with %q.
func clip(s string) string {
	const max = 64
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
