package leadctl

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadwire"
)

// constStrings returns the string value of every constant of the Go file whose
// name has the prefix, read from the source so that a constant added later is
// held too. The files read here carry build tags, which the parser ignores.
func constStrings(t *testing.T, path, prefix string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if i >= len(vs.Values) || !strings.HasPrefix(n.Name, prefix) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// callCodes returns the string literal that is the first argument of every
// call of the named functions in the Go file: the daemon's canary core makes
// some of its codes inline, as refusal("not-running", ...).
func callCodes(t *testing.T, path string, funcs ...string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(path), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || len(c.Args) == 0 {
			return true
		}
		id, ok := c.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		for _, name := range funcs {
			if id.Name != name {
				continue
			}
			if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// daemonCodes is every reason code a receipt of the daemon can carry, with
// the places they are read from: the constants of the lead extension, the
// canary core's inline codes, the message admission's codes, the holder's
// refusals the daemon prefixes with "holder-", the code a restart gives, and
// bad-message, the one code the canary core makes in a variable (for a
// message refused with an error that is not an admission's).
func daemonCodes(t *testing.T) []string {
	t.Helper()
	const dir = "../../leaddaemon/"
	var all []string
	all = append(all, leadwire.ReasonCodes()...)
	all = append(all, constStrings(t, dir+"lead_ext_linux.go", "code")...)
	all = append(all, constStrings(t, dir+"canary_ext_linux.go", "code")...)
	all = append(all, callCodes(t, dir+"canary_ext_linux.go", "refusal", "failure")...)
	all = append(all, constStrings(t, "../../leadcanary/admission.go", "Code")...)
	for _, c := range constStrings(t, "../../leadholder/wire/control.go", "Code") {
		all = append(all, "holder-"+c)
	}
	all = append(all, constStrings(t, "../../leadcontrol/table.go", "CodeInterrupted")...)
	all = append(all, "bad-message")
	sort.Strings(all)
	out := all[:0]
	for i, c := range all {
		if i == 0 || c != all[i-1] {
			out = append(out, c)
		}
	}
	if len(out) < 50 {
		t.Fatalf("read %d codes: the daemon's sources were not read", len(out))
	}
	return out
}

func TestEveryReasonCodeHasWords(t *testing.T) {
	for _, c := range daemonCodes(t) {
		if _, ok := reasonWords[c]; !ok {
			t.Errorf("the daemon sends the code %q and the view has no words for it", c)
		}
	}
	// The codes of leadwire's error answers, and of leadwire's own list.
	for _, c := range append(leadwire.ErrorCodes(), leadwire.ReasonCodes()...) {
		if _, ok := reasonWords[c]; !ok {
			t.Errorf("leadwire names the code %q and the view has no words for it", c)
		}
	}
}

// TestNoWordsForACodeNothingSends holds that every code of the table is one
// that is sent: by the daemon, by leadwire's error answers or by the
// console's adapter (not-listed, conflict and invalid-request, which it makes
// itself from an error of the client; see refusedCode in cmd/ataniq-console).
func TestNoWordsForACodeNothingSends(t *testing.T) {
	sent := map[string]bool{"not-listed": true, "conflict": true, "invalid-request": true}
	for _, c := range daemonCodes(t) {
		sent[c] = true
	}
	for _, c := range leadwire.ErrorCodes() {
		sent[c] = true
	}
	for c := range reasonWords {
		if !sent[c] {
			t.Errorf("the view has words for the code %q and nothing sends it", c)
		}
	}
}

// TestReasonWordsAreShortASCII holds that each text is plain printable ASCII
// of one line and short enough for a line of the view.
func TestReasonWordsAreShortASCII(t *testing.T) {
	for c, w := range reasonWords {
		if w == "" || len(w) > 80 {
			t.Errorf("%s: %d bytes: %q", c, len(w), w)
		}
		for i := 0; i < len(w); i++ {
			if w[i] < 0x20 || w[i] > 0x7e {
				t.Errorf("%s: %q is not printable ASCII", c, w)
				break
			}
		}
		if len(c) > 64 || safeID(c, 64) != c {
			t.Errorf("the code %q is not a short code", c)
		}
	}
}
