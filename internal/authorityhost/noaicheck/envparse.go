package noaicheck

import (
	"strings"
)

// envSyntax is the syntax a file that sets variables is read in.
type envSyntax int

const (
	syntaxShell   envSyntax = iota // a shell profile (sh, bash, zsh; csh setenv; fish set in a .fish file)
	syntaxEnvFile                  // KEY=VALUE lines: /etc/environment, environment.d, an EnvironmentFile=
	syntaxPam                      // ~/.pam_environment: NAME [DEFAULT=...] [OVERRIDE=...]
	syntaxSystemd                  // a unit file, drop-in or manager configuration
)

// maxNameLen bounds a variable name the check reports.
const maxNameLen = 128

// validName reports whether n is a variable name the check may report: a
// shell identifier of at most maxNameLen bytes. Nothing else of a file ever
// reaches a report.
func validName(n string) bool {
	if n == "" || len(n) > maxNameLen || !identStart(n[0]) {
		return false
	}
	for i := 1; i < len(n); i++ {
		if !ident(n[i]) {
			return false
		}
	}
	return true
}

func identStart(b byte) bool { return b == '_' || (b|0x20 >= 'a' && b|0x20 <= 'z') }

func ident(b byte) bool { return identStart(b) || (b >= '0' && b <= '9') }

// envParse is what a file sets: the provider-key names (validated, once
// each), the EnvironmentFile= paths it names (absolute; the last component
// may be a glob), the path words of the files a shell profile sources, and
// whether some of it could not be parsed.
type envParse struct {
	names   []string
	files   []envRef
	sourced []srcWord
	bad     bool
	seen    map[string]bool
}

// envRef is a path an EnvironmentFile= line names, as written (it may hold
// %h, %t or %%, and a glob in its last component), and whether it is
// optional (written with a leading '-'): systemd refuses to start a unit
// whose required file is missing.
type envRef struct {
	path     string
	optional bool
}

func (r *envParse) add(n string) {
	if !validName(n) || !ProviderKeyName(n) {
		return
	}
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if !r.seen[n] {
		r.seen[n] = true
		r.names = append(r.names, n)
	}
}

// parseEnv reads data in the syntax s. fish says a shell file is fish's.
func parseEnv(s envSyntax, data string, fish bool) envParse {
	var r envParse
	switch s {
	case syntaxShell:
		parseShell(&r, data, fish)
	case syntaxEnvFile:
		parseEnvFile(&r, data)
	case syntaxPam:
		parsePam(&r, data)
	case syntaxSystemd:
		parseSystemd(&r, data)
	}
	return r
}

// ---- KEY=VALUE files ----

// parseEnvFile reads KEY=VALUE lines as pam_env and systemd read them:
// blank lines and lines starting with '#' or ';' are skipped, an optional
// "export " comes first, the value may be in double or single quotes (across
// lines) and a backslash escapes the next byte. Only the key is kept. A line
// that is not of this form is syntax the check cannot parse.
func parseEnvFile(r *envParse, data string) {
	i := 0
	for i < len(data) {
		for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
			i++
		}
		if i >= len(data) {
			return
		}
		if data[i] == '#' || data[i] == ';' {
			i = lineEnd(data, i)
			continue
		}
		if strings.HasPrefix(data[i:], "export") && i+6 < len(data) && (data[i+6] == ' ' || data[i+6] == '\t') {
			i += 6
			for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
				i++
			}
		}
		j := i
		for j < len(data) && ident(data[j]) {
			j++
		}
		name := data[i:j]
		k := j
		for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
			k++
		}
		if !validName(name) || k >= len(data) || data[k] != '=' {
			r.bad = true
			i = lineEnd(data, i)
			continue
		}
		r.add(name)
		i = k + 1
		// The value: to the end of the line, quotes and escapes honoured.
	value:
		for i < len(data) {
			switch data[i] {
			case '\n':
				break value
			case '\\':
				i += 2
			case '"', '\'':
				q := data[i]
				i++
				for i < len(data) && data[i] != q {
					if data[i] == '\\' && q == '"' {
						i++
					}
					i++
				}
				if i >= len(data) {
					r.bad = true
					return
				}
				i++
			default:
				i++
			}
		}
	}
}

func lineEnd(data string, i int) int {
	if j := strings.IndexByte(data[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(data)
}

// parsePam reads ~/.pam_environment: the first word of a line (up to a
// blank or '=') is the variable, the rest is its settings.
func parsePam(r *envParse, data string) {
	for _, line := range logicalLines(data) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		name := line
		if i := strings.IndexAny(line, " \t="); i >= 0 {
			name = line[:i]
		}
		if !validName(name) {
			r.bad = true
			continue
		}
		r.add(name)
	}
}

// logicalLines joins lines ending with a backslash to the next one, as
// systemd and pam_env do; a comment line inside a continued line is
// skipped, as systemd does.
func logicalLines(data string) []string {
	var out []string
	var cur strings.Builder
	cont := false
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		t := strings.TrimSpace(line)
		if cont && (strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";")) {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			cur.WriteString(line[:len(line)-1])
			cur.WriteByte(' ')
			cont = true
			continue
		}
		cur.WriteString(line)
		out = append(out, cur.String())
		cur.Reset()
		cont = false
	}
	if cont {
		out = append(out, cur.String())
	}
	return out
}

// ---- systemd ----

// parseSystemd reads a unit file, drop-in or manager configuration:
// Environment=, DefaultEnvironment= and ManagerEnvironment= (words split
// with systemd's quoting, each NAME=VALUE; only NAME is kept),
// PassEnvironment= (names) and EnvironmentFile= (one path, a '-' first
// when it is optional, quoted or not, absolute or starting with %h or %t; a glob in its
// last component; no specifier but %h, %t and %%). Comments
// ('#', ';'), section headers and other keys are passed over. A word that
// is not NAME=VALUE, an unclosed quote or a path with a specifier is
// syntax the check cannot parse.
func parseSystemd(r *envParse, data string) {
	for _, line := range logicalLines(data) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Environment", "DefaultEnvironment", "ManagerEnvironment":
			words, ok := splitSystemd(value)
			if !ok {
				r.bad = true
				continue
			}
			for _, w := range words {
				name, _, ok := strings.Cut(w, "=")
				if !ok || !validName(name) {
					r.bad = true
					continue
				}
				r.add(name)
			}
		case "PassEnvironment":
			words, ok := splitSystemd(value)
			if !ok {
				r.bad = true
				continue
			}
			for _, w := range words {
				if !validName(w) {
					r.bad = true
					continue
				}
				r.add(w)
			}
		case "EnvironmentFile":
			if value == "" {
				continue // resets the list
			}
			p := strings.TrimPrefix(value, "-")
			optional := p != value
			if words, ok := splitSystemd(p); ok && len(words) == 1 {
				p = words[0]
				if strings.HasPrefix(p, "-") {
					p, optional = p[1:], true
				}
			} else {
				r.bad = true
				continue
			}
			if !(strings.HasPrefix(p, "/") || strings.HasPrefix(p, "%h/") || strings.HasPrefix(p, "%t/")) || !knownSpecifiers(p) {
				r.bad = true
				continue
			}
			r.files = append(r.files, envRef{path: p, optional: optional})
		}
	}
}

// knownSpecifiers reports whether every specifier in p is one the check
// resolves: %h (the home), %t (the runtime folder) or %% (a percent sign).
func knownSpecifiers(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] != '%' {
			continue
		}
		if i+1 >= len(p) || strings.IndexByte("ht%", p[i+1]) < 0 {
			return false
		}
		i++
	}
	return true
}

// splitSystemd splits a value into words as systemd does: blanks separate
// words, double and single quotes quote, a backslash escapes the next
// byte. It fails on an unclosed quote.
func splitSystemd(v string) ([]string, bool) {
	var words []string
	var w strings.Builder
	in := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == ' ' || c == '\t':
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		case c == '\\':
			in = true
			if i+1 < len(v) {
				i++
				w.WriteByte(v[i])
			}
		case c == '"' || c == '\'':
			in = true
			j := i + 1
			for j < len(v) && v[j] != c {
				if v[j] == '\\' && j+1 < len(v) {
					j++
				}
				w.WriteByte(v[j])
				j++
			}
			if j >= len(v) {
				return nil, false
			}
			i = j
		default:
			in = true
			w.WriteByte(c)
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words, true
}

// ---- shell ----

// shWord is one shell word: lit is its text with quotes removed and
// escapes applied, expansions kept as written; exp says it holds an
// expansion ($..., `...`) outside single quotes, at expAt in lit;
// assign is NAME when the word is an assignment NAME=... or NAME+=...
// written plainly (no quote or expansion before the '=').
type shWord struct {
	lit    string
	exp    bool
	expAt  int
	assign string
}

// name is the variable an argument of export (declare, typeset, readonly,
// local) names: the text before its first '=', after quote removal, and
// whether it is known (no expansion in it).
func (w shWord) name() (string, bool) {
	n := w.lit
	if i := strings.IndexByte(n, '='); i >= 0 {
		n = n[:i]
	}
	n = strings.TrimSuffix(n, "+")
	return n, !w.exp || w.expAt > len(n)
}

type shLexer struct {
	s        string
	i        int
	bad      bool
	heredocs []heredoc
}

type heredoc struct {
	delim string
	strip bool
}

const shOpChars = ";&|()<>"

var shOps = []string{"<<<", "<<-", ";;&", "&&", "||", ";;", ";&", "<<", ">>", "<&", ">&", "<>", ">|", "&>", "|&", ";", "&", "|", "(", ")", "<", ">"}

// next returns the next operator (op != "") or word; at the end both are
// empty and eof is true. A newline is the operator "\n"; the bodies of the
// here-documents started on its line are skipped after it.
func (l *shLexer) next() (op string, w shWord, eof bool) {
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			l.i++
		case c == '\\' && l.i+1 < len(l.s) && l.s[l.i+1] == '\n':
			l.i += 2
		case c == '#':
			for l.i < len(l.s) && l.s[l.i] != '\n' {
				l.i++
			}
		case c == '\n':
			l.i++
			l.bodies()
			return "\n", shWord{}, false
		case strings.IndexByte(shOpChars, c) >= 0:
			for _, op := range shOps {
				if strings.HasPrefix(l.s[l.i:], op) {
					l.i += len(op)
					return op, shWord{}, false
				}
			}
		default:
			return "", l.word(), false
		}
	}
	if len(l.heredocs) > 0 {
		l.bad = true // a here-document never ended
	}
	return "", shWord{}, true
}

// bodies skips the here-documents whose operator was on the line just
// ended.
func (l *shLexer) bodies() {
	for _, h := range l.heredocs {
		for {
			if l.i >= len(l.s) {
				l.bad = true
				return
			}
			end := lineEnd(l.s, l.i)
			line := strings.TrimSuffix(strings.TrimSuffix(l.s[l.i:end], "\n"), "\r")
			l.i = end
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
	l.heredocs = nil
}

func (l *shLexer) word() shWord {
	var lit strings.Builder
	w := shWord{expAt: -1}
	plain := true
	mark := func() {
		if !w.exp {
			w.exp, w.expAt = true, lit.Len()
		}
	}
	for l.i < len(l.s) {
		c := l.s[l.i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || strings.IndexByte(shOpChars, c) >= 0 {
			// NAME=(...) is an array assignment, one word.
			if c == '(' && w.assign != "" && strings.HasSuffix(lit.String(), "=") {
				start := l.i
				l.i = l.group(l.i, '(', ')')
				lit.WriteString(l.s[start:l.i])
				continue
			}
			break
		}
		switch c {
		case '\\':
			plain = false
			if l.i+1 < len(l.s) {
				if l.s[l.i+1] != '\n' {
					lit.WriteByte(l.s[l.i+1])
				}
				l.i += 2
			} else {
				l.i++
			}
		case '\'':
			plain = false
			j := strings.IndexByte(l.s[l.i+1:], '\'')
			if j < 0 {
				l.bad = true
				l.i = len(l.s)
				break
			}
			lit.WriteString(l.s[l.i+1 : l.i+1+j])
			l.i += j + 2
		case '"':
			plain = false
			l.i = l.dquote(l.i+1, &lit, mark)
		case '$', '`':
			plain = false
			mark()
			start := l.i
			l.i = l.expansion(l.i)
			lit.WriteString(l.s[start:l.i])
		case '=':
			if plain && w.assign == "" {
				n := strings.TrimSuffix(lit.String(), "+")
				if validName(n) {
					w.assign = n
				}
			}
			plain = false
			lit.WriteByte(c)
			l.i++
		default:
			lit.WriteByte(c)
			l.i++
		}
	}
	w.lit = lit.String()
	return w
}

// dquote reads a double-quoted string from i (after the quote) into lit
// and returns the position after the closing quote.
func (l *shLexer) dquote(i int, lit *strings.Builder, mark func()) int {
	for i < len(l.s) {
		switch c := l.s[i]; c {
		case '"':
			return i + 1
		case '\\':
			if i+1 < len(l.s) {
				switch n := l.s[i+1]; n {
				case '$', '`', '"', '\\':
					lit.WriteByte(n)
				case '\n':
				default:
					lit.WriteByte('\\')
					lit.WriteByte(n)
				}
				i += 2
			} else {
				i++
			}
		case '$', '`':
			mark()
			start := i
			i = l.expansion(i)
			lit.WriteString(l.s[start:i])
		default:
			lit.WriteByte(c)
			i++
		}
	}
	l.bad = true
	return len(l.s)
}

// expansion skips the expansion at i ($NAME, ${...}, $(...), $((...)),
// $'...', `...`) and returns the position after it.
func (l *shLexer) expansion(i int) int {
	if l.s[i] == '`' {
		for j := i + 1; j < len(l.s); j++ {
			switch l.s[j] {
			case '\\':
				j++
			case '`':
				return j + 1
			}
		}
		l.bad = true
		return len(l.s)
	}
	if i+1 >= len(l.s) {
		return i + 1
	}
	switch l.s[i+1] {
	case '(':
		return l.group(i+1, '(', ')')
	case '{':
		return l.group(i+1, '{', '}')
	case '\'':
		for j := i + 2; j < len(l.s); j++ {
			switch l.s[j] {
			case '\\':
				j++
			case '\'':
				return j + 1
			}
		}
		l.bad = true
		return len(l.s)
	}
	return i + 1
}

// group skips from the opening byte at i to its matching close, over
// quotes and nested expansions, and returns the position after it. A bare
// "{" inside a ${...} does not nest.
func (l *shLexer) group(i int, open, close byte) int {
	depth := 0
	for i < len(l.s) {
		c := l.s[i]
		switch {
		case c == '\\':
			i += 2
			continue
		case c == '\'':
			j := strings.IndexByte(l.s[i+1:], '\'')
			if j < 0 {
				l.bad = true
				return len(l.s)
			}
			i += j + 2
			continue
		case c == '"':
			var sink strings.Builder
			i = l.dquote(i+1, &sink, func() {})
			continue
		case c == '`' || (c == '$' && i+1 < len(l.s) && (l.s[i+1] == '(' || l.s[i+1] == '{')):
			i = l.expansion(i)
			continue
		case c == open && (open == '(' || depth == 0):
			// Parentheses nest; inside ${...} only a nested ${ does, as
			// in bash ("${x:-{a}}" ends at the first "}").
			depth++
		case c == close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	l.bad = true
	return len(l.s)
}

// shReserved are the words after which a command starts again.
var shReserved = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true,
	"while": true, "until": true, "esac": true, "{": true, "}": true, "!": true, "time": true,
}

// srcWord is the path word of a "." or "source" command: its text with
// quotes removed and expansions kept as written, and whether it holds an
// expansion (a word without one is a literal path, "$" and all).
type srcWord struct {
	lit string
	exp bool
}

// forList is what a for loop's variable takes: the words of its list, or,
// when there is no list ("for x; do", the positional parameters), none it
// can name (ok false).
type forList struct {
	words []srcWord
	ok    bool
}

// parseShell reads a shell profile as a token stream (quotes may span
// lines; here-documents are skipped; comments start at a word). It keeps
// the names of the assignments at the start of a command (NAME=value
// cmd ... included), the names that export, declare, typeset, readonly
// and env set, the first argument of setenv (csh) and, in a fish file, of
// set. A value is never read for names.
//
// A function body (NAME() { ... }, function NAME { ... }) is followed by
// its braces: local, declare, typeset and readonly in it declare the
// function's own variables, not the login environment's, and are passed
// over, as are a local outside a function (an error in bash) and a
// declare, typeset or readonly whose name is an expansion. An export
// whose name is an expansion, an unclosed quote or substitution, or a
// here-document that never ends is syntax the check cannot parse.
//
// The path word of every "." and "source" command is returned (sourced),
// to be resolved by the caller (see Checker.sourcePaths); a word that is
// exactly the variable of an earlier "for NAME in WORDS" loop stands for
// each of those words (". "$profile"" in a loop over /etc/profile.d/*.sh).
func parseShell(r *envParse, data string, fish bool) {
	l := &shLexer{s: data}
	const (
		argNone = iota
		argExport
		argDeclare
		argIgnore
		argEnv
		argFirst
		argSource
		argFunc
		argForName
		argForIn
		argForList
	)
	cmdPos, args, redirect, here, strip := true, argNone, false, false, false
	// Function bodies: braces is the stack of open { } groups, each saying
	// whether it is a function's body; hdr follows NAME ( ) (1 after NAME,
	// 2 after its "("), and pending says the next { opens a function body.
	var braces []bool
	hdr, pending := 0, false
	inFunc := func() bool {
		for _, f := range braces {
			if f {
				return true
			}
		}
		return false
	}
	loops := map[string]forList{}
	var forVar string
	var forWords []srcWord
	endFor := func(ok bool) {
		if args == argForIn || args == argForList {
			loops[forVar] = forList{words: forWords, ok: ok && args == argForList}
		}
	}
	for {
		op, w, eof := l.next()
		if eof {
			break
		}
		if op != "" {
			switch op {
			case "<<", "<<-":
				here, strip = true, op == "<<-"
			case "<", ">", ">>", "<&", ">&", "<>", ">|", "&>", "<<<":
				redirect = true
			case "(":
				if hdr == 1 {
					hdr = 2
					continue
				}
				hdr, pending = 0, false
				endFor(false)
				cmdPos, args = true, argNone
			case ")":
				if hdr == 2 {
					hdr, pending = 0, true
					continue
				}
				hdr, pending = 0, false
				endFor(false)
				cmdPos, args = true, argNone
			case "\n":
				hdr = 0
				endFor(true)
				cmdPos, args = true, argNone
			default:
				hdr, pending = 0, false
				endFor(true)
				cmdPos, args = true, argNone
			}
			continue
		}
		hdr = 0
		switch {
		case here:
			l.heredocs = append(l.heredocs, heredoc{delim: w.lit, strip: strip})
			here = false
			continue
		case redirect:
			redirect = false
			continue
		case !w.exp && w.lit == "{":
			if cmdPos || pending {
				braces = append(braces, pending)
			}
			pending = false
			cmdPos, args = true, argNone
			continue
		case !w.exp && w.lit == "}":
			if cmdPos && len(braces) > 0 {
				braces = braces[:len(braces)-1]
			}
			pending = false
			cmdPos, args = true, argNone
			continue
		}
		pending = false
		if cmdPos {
			if w.assign != "" {
				r.add(w.assign)
				continue
			}
			if !w.exp && shReserved[w.lit] {
				continue
			}
			cmdPos, args = false, argNone
			if w.exp {
				continue
			}
			hdr = 1 // NAME of NAME ( ) { ... }, when "(" follows
			switch w.lit {
			case "export":
				args = argExport
			case "declare", "typeset", "readonly":
				args = argDeclare
				if inFunc() {
					args = argIgnore
				}
			case "local":
				args = argIgnore
			case "env":
				args = argEnv
			case "setenv":
				args = argFirst
			case "set":
				if fish {
					args = argFirst
				}
			case ".", "source":
				args = argSource
			case "function":
				args = argFunc
			case "for":
				args = argForName
			}
			continue
		}
		switch args {
		case argExport, argDeclare:
			if !w.exp && (strings.HasPrefix(w.lit, "-") || strings.HasPrefix(w.lit, "+")) {
				continue
			}
			n, known := w.name()
			switch {
			case known:
				r.add(n)
			case args == argExport:
				r.bad = true
			}
		case argEnv:
			if w.assign != "" {
				r.add(w.assign)
			}
		case argFirst:
			if !w.exp && strings.HasPrefix(w.lit, "-") {
				continue
			}
			if !w.exp {
				r.add(w.lit)
			}
			args = argNone
		case argSource:
			if n, ok := plainRef(w); ok {
				if fl, found := loops[n]; found && fl.ok {
					r.sourced = append(r.sourced, fl.words...)
					args = argNone
					continue
				}
			}
			r.sourced = append(r.sourced, srcWord{lit: w.lit, exp: w.exp})
			args = argNone
		case argFunc:
			// function NAME [()] { ... }
			hdr, pending, args = 1, true, argNone
		case argForName:
			forVar, forWords, args = w.lit, nil, argNone
			if !w.exp && validName(w.lit) {
				args = argForIn
			}
		case argForIn:
			if !w.exp && w.lit == "in" {
				args = argForList
				continue
			}
			endFor(false)
			args = argNone
		case argForList:
			forWords = append(forWords, srcWord{lit: w.lit, exp: w.exp})
		}
	}
	if l.bad {
		r.bad = true
	}
}

// plainRef returns NAME when the word is exactly $NAME or ${NAME}.
func plainRef(w shWord) (string, bool) {
	if !w.exp || w.expAt != 0 {
		return "", false
	}
	s := w.lit
	switch {
	case strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}"):
		s = s[2 : len(s)-1]
	case strings.HasPrefix(s, "$"):
		s = s[1:]
	default:
		return "", false
	}
	return s, validName(s)
}
