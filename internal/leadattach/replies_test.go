package leadattach

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTheCockpitMakesItsLivePaneWithoutReplies keeps the premise of the
// package doc: a replica's answers to the session's queries are never sent to
// the session, so the cockpit's pane has no Replies function.
func TestTheCockpitMakesItsLivePaneWithoutReplies(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "../terminalconsole/live.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "New" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "sessionpane" {
			return true
		}
		found = true
		for _, a := range call.Args {
			lit, ok := a.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Replies" {
						t.Error("the cockpit makes its live pane with a Replies function")
					}
				}
			}
		}
		return true
	})
	if !found {
		t.Fatal("no sessionpane.New call in the cockpit's live.go: this test needs updating")
	}
}
