package genesis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
)

// gateWord is the word the seat gate answers a refusal of the brief or the
// walker with. The empty word says the refusal is not the owner's mistake but
// the host's own fault, which the gate answers "failed" and logs: a catalog
// that does not load, a brief of another catalog version, a brief that breaks
// its own rules.
//
// A refusal of what the owner sent is one of three words: the text looks like
// a secret (genesis-secret-shaped), something is over a cap (genesis-over-cap),
// or the answer is not one the question allows (genesis-bad-answer). The three
// others of the wire are of the host's state, not of any refusal of these
// packages: genesis-busy (a start past two open threads), genesis-unknown-thread
// and genesis-done (a call to a thread that is finished).
var gateWord = map[brief.Code]string{
	brief.CodeSecretShaped: ownerwire.CodeGenesisSecretShaped,
	brief.CodeTooLarge:     ownerwire.CodeGenesisOverCap,

	brief.CodeNodeUnknown:     ownerwire.CodeGenesisBadAnswer,
	brief.CodeNodeNotEligible: ownerwire.CodeGenesisBadAnswer,
	brief.CodeAnswerInvalid:   ownerwire.CodeGenesisBadAnswer,
	brief.CodeRoundInvalid:    ownerwire.CodeGenesisBadAnswer,
	brief.CodeValueInvalid:    ownerwire.CodeGenesisBadAnswer,
	brief.CodeFieldName:       ownerwire.CodeGenesisBadAnswer,

	brief.CodeBriefInvalid:    "",
	brief.CodeDomainMismatch:  "",
	brief.CodeNotInferred:     "",
	brief.CodeCatalogInvalid:  "",
	brief.CodeCatalogMismatch: "",
}

// hostStateWords are the gate words of seat.genesis that no refusal of the
// brief or the walker produces: the host's own state.
var hostStateWords = []string{ownerwire.CodeGenesisBusy, ownerwire.CodeGenesisUnknownThread, ownerwire.CodeGenesisDone}

// declaredCodes reads brief/code.go and returns every constant of type Code
// with its value, so that a code added there is found without this test being
// edited.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "brief/code.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "Code" {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out[name.Name] = v
			}
		}
	}
	return out
}

// Every refusal code of the genesis packages has its place in the map to the
// gate's words, and nothing else is in it: a code added to brief/code.go
// fails here until it is mapped, so a refusal is never answered with a word
// nobody chose.
func TestEveryRefusalCodeHasItsGateWord(t *testing.T) {
	declared := declaredCodes(t)
	if len(declared) < 13 {
		t.Fatalf("read %d codes from brief/code.go, want all of them", len(declared))
	}
	byValue := map[string]string{}
	for name, value := range declared {
		byValue[value] = name
	}
	for name, value := range declared {
		if _, ok := gateWord[brief.Code(value)]; !ok {
			t.Errorf("the refusal code %s (%q) is not mapped to a gate word, or to none", name, value)
		}
	}
	for code := range gateWord {
		if _, ok := byValue[string(code)]; !ok {
			t.Errorf("the map holds %q, which brief/code.go does not declare", code)
		}
	}
}

// The words are the gate's: each is of the gate's grammar and is declared in
// ownerwire, and the six words of seat.genesis are each the word of at least
// one refusal or of the host's state, and none twice.
func TestTheGateWordsOfGenesisAreAllUsedOnce(t *testing.T) {
	grammar := regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	all := map[string]bool{
		ownerwire.CodeGenesisBusy: false, ownerwire.CodeGenesisUnknownThread: false, ownerwire.CodeGenesisBadAnswer: false,
		ownerwire.CodeGenesisOverCap: false, ownerwire.CodeGenesisSecretShaped: false, ownerwire.CodeGenesisDone: false,
	}
	for code, word := range gateWord {
		if word == "" {
			continue
		}
		if _, ok := all[word]; !ok {
			t.Errorf("%q is mapped to %q, which is not a word of seat.genesis", code, word)
		}
		all[word] = true
	}
	for _, w := range hostStateWords {
		if all[w] {
			t.Errorf("%q is a word of the host's state and also the word of a refusal", w)
		}
		all[w] = true
	}
	var words []string
	for w, used := range all {
		words = append(words, w)
		if !used {
			t.Errorf("the gate word %q has no refusal and is not of the host's state", w)
		}
		if !grammar.MatchString(w) {
			t.Errorf("the gate word %q is not of the grammar [a-z0-9-]{1,40}", w)
		}
	}
	sort.Strings(words)
	t.Logf("the words of seat.genesis: %v", words)
	if len(words) != 6 {
		t.Errorf("%d gate words, want 6", len(words))
	}
}
