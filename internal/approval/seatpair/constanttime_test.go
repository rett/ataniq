package seatpair

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The comparison of a presented code, and of a pending id, with the one that is
// kept is of the digests of both, in constant time. A behavioural test cannot
// tell crypto/subtle.ConstantTimeCompare from ==, so the source is read: in each
// function named in want, a call of subtle.ConstantTimeCompare must be there
// with a slice of an array on each side, the file must import crypto/subtle, and
// no == or != in the file may have an operand that names a digest, a compared
// value or the like, and neither may bytes.Equal or strings.EqualFold be
// called. The checker is run on mutations too, so a change that weakens it is
// seen.
var constantTimeWhere = map[string][]string{
	"code.go":    {"check"},
	"pairing.go": {"sameID"},
}

// secretWords are the names of what is compared; an operand of == or != that
// names one is a comparison that is not in constant time.
var secretWords = []string{"digest", "want", "da", "db", "pendingID", "presented"}

func constantTimeProblems(name, src string, funcs []string) []string {
	var out []string
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return []string{err.Error()}
	}
	imports := false
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "crypto/subtle" {
			imports = true
		}
	}
	if !imports {
		out = append(out, name+" does not import crypto/subtle")
	}
	text := func(e ast.Expr) string {
		var b bytes.Buffer
		_ = printer.Fprint(&b, fset, e)
		return b.String()
	}
	named := func(e ast.Expr) bool {
		hit := false
		ast.Inspect(e, func(n ast.Node) bool {
			var id string
			switch v := n.(type) {
			case *ast.CallExpr:
				// The result of the constant-time call is a count, 1 or 0:
				// comparing it is the use of the call, not a comparison of
				// what it was given.
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
					return false
				}
				return true
			case *ast.Ident:
				id = v.Name
			case *ast.SelectorExpr:
				id = v.Sel.Name
			default:
				return true
			}
			for _, w := range secretWords {
				if id == w {
					hit = true
				}
			}
			return true
		})
		return hit
	}
	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BinaryExpr:
			if (v.Op == token.EQL || v.Op == token.NEQ) && (named(v.X) || named(v.Y)) {
				out = append(out, "a comparison with "+v.Op.String()+" of "+text(v.X)+" and "+text(v.Y))
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok {
					switch x.Name + "." + sel.Sel.Name {
					case "bytes.Equal", "strings.EqualFold", "strings.Compare", "bytes.Compare":
						out = append(out, x.Name+"."+sel.Sel.Name+" is called")
					}
				}
			}
		}
		return true
	})
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		for _, want := range funcs {
			if fn.Name.Name != want {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ConstantTimeCompare" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "subtle" || len(call.Args) != 2 {
					return true
				}
				slices := 0
				for _, a := range call.Args {
					if _, ok := a.(*ast.SliceExpr); ok {
						slices++
					}
				}
				if slices == 2 {
					found[want] = true
				}
				return true
			})
		}
	}
	for _, fn := range funcs {
		if !found[fn] {
			out = append(out, fn+" does not call subtle.ConstantTimeCompare on two slices of digests")
		}
	}
	return out
}

func TestTheComparisonsAreConstantTimeInTheSource(t *testing.T) {
	for name, funcs := range constantTimeWhere {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if problems := constantTimeProblems(name, string(src), funcs); len(problems) != 0 {
			t.Errorf("%s: %s", name, strings.Join(problems, "; "))
		}
	}
}

// The checker finds each way of weakening the comparison.
func TestTheConstantTimeCheckerFindsAWeakenedComparison(t *testing.T) {
	code, err := os.ReadFile("code.go")
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := os.ReadFile("pairing.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		name, file, from, to string
		funcs                []string
	}{
		{"an array compared with ==", "code.go", "subtle.ConstantTimeCompare(want[:], c.digest[:]) == 1", "want == c.digest", []string{"check"}},
		{"an array compared with !=", "code.go", "subtle.ConstantTimeCompare(want[:], c.digest[:]) == 1", "want != c.digest", []string{"check"}},
		{"bytes.Equal", "code.go", "subtle.ConstantTimeCompare(want[:], c.digest[:]) == 1", "bytes.Equal(want[:], c.digest[:])", []string{"check"}},
		{"the texts compared", "code.go", "subtle.ConstantTimeCompare(want[:], c.digest[:]) == 1", "presented == \"\"", []string{"check"}},
		{"a compare of whole texts", "code.go", "subtle.ConstantTimeCompare(want[:], c.digest[:])", "subtle.ConstantTimeCompare([]byte(presented), []byte(presented))", []string{"check"}},
		{"ids compared with ==", "pairing.go", "subtle.ConstantTimeCompare(da[:], db[:]) == 1", "a == b && da == db", []string{"sameID"}},
		{"the import removed", "code.go", "\"crypto/subtle\"", "\"io\"", []string{"check"}},
	} {
		t.Run(m.name, func(t *testing.T) {
			src := string(code)
			if m.file == "pairing.go" {
				src = string(pairing)
			}
			if !strings.Contains(src, m.from) {
				t.Fatalf("%s does not contain %q: the mutation tests nothing", m.file, m.from)
			}
			mutated := strings.Replace(src, m.from, m.to, 1)
			if m.name == "the import removed" {
				mutated = strings.Replace(mutated, "\t\"io\"\n", "", 1)
				mutated = strings.Replace(mutated, "\"io\"", "\"io\"\n\t\"bytes\"", 1)
			}
			if problems := constantTimeProblems(m.file, mutated, m.funcs); len(problems) == 0 {
				t.Errorf("the checker saw nothing wrong with %s", m.name)
			}
		})
	}
}
