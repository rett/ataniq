package verify

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan"
)

// Exit statuses of the script's own failures. A gate may exit with the same
// number; see the package comment.
const (
	// ExitTipMismatch is the status when the tip ref does not resolve to
	// the reviewed tip.
	ExitTipMismatch = 90
	// ExitFetchFailed is the status when the scratch repository could not
	// be made or the tip ref could not be fetched from the mirror.
	ExitFetchFailed = 91
	// ExitCheckoutFailed is the status when the tip could not be checked
	// out.
	ExitCheckoutFailed = 92
	// ExitNotMerge is the status of a merge-mode script when the fetched
	// commit is not a commit of exactly two parents whose first is the
	// recorded base.
	ExitNotMerge = 93
)

// PendingPrefix is the namespace of the pins of host-built merges: the refs
// that a request in merge mode may name. It is the prefix of package
// mergebuild's pins and of the intake's own constant of the same name; a test
// holds them equal.
const PendingPrefix = "refs/ataniq/pending/"

// MarkerPrefix starts the line the script prints before each gate. The line
// is "ataniq-verify: gate <name>".
const MarkerPrefix = "ataniq-verify: gate "

const (
	tipLocalRef  = "refs/verify/tip"
	leadsPrefix  = "refs/leads/"
	maxGates     = 64
	maxGateArgs  = 1024
	maxGateBytes = 64 << 10
)

// Gate is one check run in the root of the scratch clone.
type Gate struct {
	// Name is [a-z0-9][a-z0-9-]{0,62}. It is printed in the marker line.
	Name string
	// Argv is the command and its arguments. Each element becomes one word:
	// nothing is expanded by the shell. Argv[0] is an absolute path, or a
	// plain command name (no slash) that is looked up on the sandbox's
	// PATH. It is not a name the shell runs by itself: a shell builtin
	// such as cd, exit or eval, or a reserved word, is refused when the
	// request is validated. No element holds a NUL byte or a newline, and
	// Argv[0] does not start with a dash.
	Argv []string
}

// shellOwnNames are the names a POSIX shell runs by itself, with no program
// behind them: the special builtins, the regular builtins that change or read
// the shell's own state, common extensions of other shells, and the reserved
// words. A gate runs as an external program (see [Script]), so such a name
// could only fail with status 127 inside the sandbox; it is refused when the
// request is validated instead. Builtins that also exist as a program (true,
// false, echo, printf, test, pwd, kill) are not listed: they run as that
// program.
var shellOwnNames = map[string]bool{
	// POSIX special builtins.
	".": true, ":": true, "break": true, "continue": true, "eval": true,
	"exec": true, "exit": true, "export": true, "readonly": true,
	"return": true, "set": true, "shift": true, "times": true, "trap": true,
	"unset": true,
	// POSIX regular builtins that act on the shell itself.
	"alias": true, "bg": true, "cd": true, "command": true, "fc": true,
	"fg": true, "getopts": true, "hash": true, "jobs": true, "read": true,
	"type": true, "ulimit": true, "umask": true, "unalias": true, "wait": true,
	// Common extensions.
	"source": true, "builtin": true, "local": true, "declare": true,
	"typeset": true, "let": true, "shopt": true, "pushd": true, "popd": true,
	"dirs": true, "disown": true, "enable": true, "history": true,
	"bind": true, "caller": true, "compgen": true, "complete": true,
	"mapfile": true, "readarray": true, "suspend": true, "logout": true,
	"whence": true, "where": true, "functions": true, "autoload": true,
	"integer": true, "float": true,
	// Reserved words.
	"!": true, "{": true, "}": true, "[[": true, "]]": true, "if": true,
	"then": true, "elif": true, "else": true, "fi": true, "for": true,
	"while": true, "until": true, "do": true, "done": true, "case": true,
	"esac": true, "in": true, "select": true, "function": true, "time": true,
	"coproc": true,
}

// checkCommand reports why name cannot be a gate's command.
func checkCommand(name string) error {
	switch {
	case strings.HasPrefix(name, "-"):
		return errors.New("the command starts with a dash")
	case strings.HasPrefix(name, "/"):
		return nil
	case strings.Contains(name, "/"):
		return errors.New("the command is neither an absolute path nor a plain name without a slash")
	case shellOwnNames[name]:
		return fmt.Errorf("%q is a shell builtin or reserved word, not a program", name)
	}
	return nil
}

func validateGates(gates []Gate) error {
	if len(gates) == 0 {
		return errors.New("verify: no gates")
	}
	if len(gates) > maxGates {
		return fmt.Errorf("verify: more than %d gates", maxGates)
	}
	seen := make(map[string]bool, len(gates))
	for i, g := range gates {
		if !sandboxplan.ValidName(g.Name) {
			return fmt.Errorf("verify: gate %d: name %q is not [a-z0-9][a-z0-9-]{0,62}", i, g.Name)
		}
		if seen[g.Name] {
			return fmt.Errorf("verify: gate name %q is used twice", g.Name)
		}
		seen[g.Name] = true
		if len(g.Argv) == 0 || g.Argv[0] == "" {
			return fmt.Errorf("verify: gate %q: empty command", g.Name)
		}
		if err := checkCommand(g.Argv[0]); err != nil {
			return fmt.Errorf("verify: gate %q: %w", g.Name, err)
		}
		if len(g.Argv) > maxGateArgs {
			return fmt.Errorf("verify: gate %q: more than %d arguments", g.Name, maxGateArgs)
		}
		size := 0
		for j, a := range g.Argv {
			if strings.ContainsAny(a, "\x00\n") {
				return fmt.Errorf("verify: gate %q: argument %d holds a NUL byte or a newline", g.Name, j)
			}
			size += len(a)
		}
		if size > maxGateBytes {
			return fmt.Errorf("verify: gate %q: arguments longer than %d bytes", g.Name, maxGateBytes)
		}
	}
	return nil
}

// PlanDigest returns the hex SHA-256 of a canonical encoding of the gates:
// a fixed label, the number of gates, and for each gate its name and its
// arguments, every string with its length in front. The digest changes with
// the order of the gates, their names and their arguments. It does not check
// the gates.
func PlanDigest(gates []Gate) string {
	h := sha256.New()
	var buf [binary.MaxVarintLen64]byte
	num := func(n int) {
		h.Write(buf[:binary.PutUvarint(buf[:], uint64(n))])
	}
	str := func(s string) {
		num(len(s))
		h.Write([]byte(s))
	}
	str("ataniq-verify-gates-v1")
	num(len(gates))
	for _, g := range gates {
		str(g.Name)
		num(len(g.Argv))
		for _, a := range g.Argv {
			str(a)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkTip reports why tip is not a full object id.
func checkTip(tip string) error {
	if !gitenv.IsObjectID(tip) {
		return errors.New("verify: tip is not a full lower-case object id")
	}
	return nil
}

// checkTipRef reports why ref cannot be the ref to verify. In lead mode it
// must be under refs/leads/ and only there; in merge mode it must be under
// [PendingPrefix] and only there. In both it must be a valid ref name by the
// rules of git check-ref-format.
func checkTipRef(ref string, merge bool) error {
	prefix := leadsPrefix
	if merge {
		prefix = PendingPrefix
	}
	if !strings.HasPrefix(ref, prefix) || len(ref) == len(prefix) {
		return fmt.Errorf("verify: tip ref %q is not under %s", ref, prefix)
	}
	if len(ref) > 1024 {
		return errors.New("verify: tip ref is longer than 1024 bytes")
	}
	if strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, "/") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return fmt.Errorf("verify: tip ref %q is not a valid ref name", ref)
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return fmt.Errorf("verify: tip ref %q is not a valid ref name", ref)
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("verify: tip ref %q is not a valid ref name", ref)
		}
	}
	return nil
}

// checkInsidePath reports why p cannot be named in the script: it must be an
// absolute, clean path other than "/", without NUL or newline.
func checkInsidePath(what, p string) error {
	switch {
	case p == "" || !filepath.IsAbs(p):
		return fmt.Errorf("verify: %s %q is not an absolute path", what, p)
	case strings.ContainsAny(p, "\x00\n"):
		return fmt.Errorf("verify: %s holds a NUL byte or a newline", what)
	case filepath.Clean(p) != p || p == "/":
		return fmt.Errorf("verify: %s %q is not a clean path below /", what, p)
	}
	return nil
}

// Script returns the text that /bin/sh -c runs inside the sandbox. It is
// fixed text: every part that varies is one single-quoted word made by
// [sandboxplan.ShellQuote], so nothing in it is expanded or split by the
// shell. mirrorInside and scratchInside are the folders as the inner command
// sees them.
//
// The script makes a repository in <scratchInside>/tree, fetches tipRef from
// the mirror into refs/verify/tip with no tags and no shared objects, checks
// that it is exactly tip (exit 90 if not), checks it out detached, and then,
// for each gate in order, prints "ataniq-verify: gate <name>" and runs the
// gate as a program, in a subshell, from the tree's root. The first gate that
// fails ends the script with its status. Standard error is joined to standard output.
func Script(tipRef, tip string, mirrorInside, scratchInside string, gates []Gate) (string, error) {
	return makeScript(false, tipRef, tip, "", mirrorInside, scratchInside, gates)
}

// MergeScript is [Script] for merge mode: tipRef is a pin under
// [PendingPrefix] and base is the target's tip the merge was built against.
// After the fetch, and before any gate, the script checks that the fetched
// commit is tip, and that it has exactly two parents and the first is base;
// otherwise it ends with [ExitNotMerge] and no gate runs.
func MergeScript(tipRef, tip, base string, mirrorInside, scratchInside string, gates []Gate) (string, error) {
	return makeScript(true, tipRef, tip, base, mirrorInside, scratchInside, gates)
}

func makeScript(merge bool, tipRef, tip, base string, mirrorInside, scratchInside string, gates []Gate) (string, error) {
	if err := checkTipRef(tipRef, merge); err != nil {
		return "", err
	}
	if err := checkTip(tip); err != nil {
		return "", err
	}
	if err := checkBase(merge, tip, base); err != nil {
		return "", err
	}
	if err := checkInsidePath("mirror path", mirrorInside); err != nil {
		return "", err
	}
	if err := checkInsidePath("scratch path", scratchInside); err != nil {
		return "", err
	}
	if err := validateGates(gates); err != nil {
		return "", err
	}
	return buildScriptMode(merge, tipRef, tip, base, mirrorInside, scratchInside, gates)
}

// checkBase reports why base is not acceptable for the mode: in merge mode a
// full object id of the format of tip, in lead mode empty.
func checkBase(merge bool, tip, base string) error {
	if !merge {
		if base != "" {
			return errors.New("verify: a base is for merge mode only")
		}
		return nil
	}
	if !gitenv.IsObjectID(base) || len(base) != len(tip) {
		return errors.New("verify: base is not a full lower-case object id of the format of the tip")
	}
	return nil
}

// buildScript writes the text of [Script] for arguments that were checked.
// It is separate so that a test can show what the script does with a gate
// that validation would refuse.
func buildScript(tipRef, tip string, mirrorInside, scratchInside string, gates []Gate) (string, error) {
	return buildScriptMode(false, tipRef, tip, "", mirrorInside, scratchInside, gates)
}

// buildScriptMode is [buildScript] with the merge-mode check of the fetched
// commit's parents when merge is true.
func buildScriptMode(merge bool, tipRef, tip, base string, mirrorInside, scratchInside string, gates []Gate) (string, error) {
	q := func(s string) string {
		out, err := sandboxplan.ShellQuote([]string{s})
		if err != nil {
			// Unreachable: the inputs hold no NUL byte.
			panic(err)
		}
		return out
	}
	format := "sha1"
	if len(tip) == 64 {
		format = "sha256"
	}
	fail := func(status int, msg string) string {
		return fmt.Sprintf("|| { echo %s; exit %d; }\n", q("ataniq-verify: "+msg), status)
	}

	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("exec 2>&1\n")
	b.WriteString("GIT_CONFIG_NOSYSTEM=1\nGIT_TERMINAL_PROMPT=0\nexport GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT\n")
	b.WriteString("tree=" + q(scratchInside+"/tree") + "\n")
	b.WriteString(`git -c init.defaultBranch=verify init --quiet --template= --object-format=` + format + ` -- "$tree" `)
	b.WriteString(fail(ExitFetchFailed, "cannot make the scratch repository"))
	b.WriteString(`cd "$tree" `)
	b.WriteString(fail(ExitFetchFailed, "cannot enter the scratch repository"))
	b.WriteString("git fetch --quiet --no-tags -- " + q(mirrorInside) + " " + q("+"+tipRef+":"+tipLocalRef) + " ")
	b.WriteString(fail(ExitFetchFailed, "cannot fetch the tip ref from the mirror"))
	b.WriteString(`[ "$(git rev-parse --verify --quiet ` + tipLocalRef + `)" = ` + q(tip) + ` ] `)
	b.WriteString(fail(ExitTipMismatch, "the tip ref does not resolve to the reviewed tip"))
	if merge {
		// The parents of the fetched commit: its own id, then each parent.
		// Exactly two words after the id, the first the base. A failed
		// listing leaves no words, which is refused as well.
		b.WriteString("set -- $(git rev-list --parents -n 1 " + tipLocalRef + " 2>/dev/null || true)\n")
		b.WriteString(`[ "$#" -eq 3 ] && [ "$2" = ` + q(base) + ` ] `)
		b.WriteString(fail(ExitNotMerge, "the tip is not a merge whose first parent is the base"))
	}
	b.WriteString("git checkout --quiet --detach " + tipLocalRef + " ")
	b.WriteString(fail(ExitCheckoutFailed, "cannot check out the tip"))
	b.WriteString("unset GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT\n")
	for _, g := range gates {
		b.WriteString("printf '%s\\n' " + q(MarkerPrefix+g.Name) + "\n")
		line, err := sandboxplan.ShellQuote(g.Argv)
		if err != nil {
			return "", fmt.Errorf("verify: gate %q: %w", g.Name, err)
		}
		// A subshell that execs: a builtin or a function is never run in
		// this shell, nothing a gate does changes this shell's state, and
		// every gate starts in the tree's root.
		b.WriteString(`( cd "$tree" && exec ` + line + " ) || exit $?\n")
	}
	return b.String(), nil
}
